package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Global settings keys stored in settings; no new table needed.
const (
	// settingNotifyEnabled is the master switch, enabled by default for quick maintenance shutdown.
	// Channels being configured is what actually enables the feature.
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the public URL for finding detail links
	// (for example https://artex.example.com); empty omits the link button.
	// No other public URL setting is available to reuse.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the digest period in minutes.
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick sets polling frequency. Three seconds bounds notification responsiveness
	// and accounts for most of the delay between saving a finding and reaching IM.
	notifyTick = 3 * time.Second
	// notifyLease is the delivery claim lease and must substantially exceed the worst
	// single send time (the notify HTTP client times out after 15 seconds),
	// or two dispatchers may deliver the same row concurrently.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick caps events dispatched per tick, preventing all historical
	// backlog from expanding into delivery tasks at once when a channel is enabled.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest interval.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick caps per-tick delivery for unlimited channels.
	// It prevents an unlimited channel with thousands of findings
	// from blocking one loop iteration for too long.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick caps sends per channel and tick.
	//
	// Derive this cap from the lease (notifyLease = 3 minutes). If worst-case serial
	// send time exceeds the lease, later rows expire before they are sent.
	// One process is safe because Run is serial and ticks do not reenter, but with two
	// processes sharing a database, expired rows can be reclaimed and sent twice.
	// attempts also increments twice and may mark a still-running delivery failed.
	//
	// Three minutes / 30 seconds per send = 6 uses the entire lease without margin.
	// Use 5 for 150 seconds worst case and 30 seconds slack. This relation is pinned by
	// TestNotifyTickBudgetFitsWithinLease: changing notifyLease,
	// notifySendTimeout, or this cap can break its assertion.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout caps each delivery and determines the send cap above.
	// Their product must remain below notifyLease; see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff lists failure retry delays, indexed by completed attempts.
// Three attempts including the initial send match db.MaxNotifyAttempts; change both together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier is the finding notification delivery engine.
//
// It runs in its own goroutine alongside Scheduler (see server.New).
// It has its own tick because three-second notification latency differs from trigger scheduling,
// and a blocked notification must not affect agent triggers.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu protects buckets. Few channels and low contention need only one mutex,
	// without a finer-grained structure.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a per-channel token bucket.
//
// Token buckets avoid fixed-window boundary bursts: 20 messages at the end of
// one minute followed by 20 immediately in the next can send 40 in one second.
// Steady replenishment avoids provider rate limiting caused by that burst.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx ends; server.New starts it once.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step dispatches new events, then sends due deliveries.
//
// Failures only log and must not become process-level errors.
// Each tick is independent and naturally retries on the next iteration.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] Failed to dispatch events: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] Failed to read channels: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// Tokens count messages (HTTP requests), not findings.
		// Realtime uses one message per finding; digest packs a batch
		// into one message, consuming one token.
		//
		// Both modes acquire tokens before claiming; reversing this order would make
		// rate-limited deliveries consume retry attempts.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns this tick's token cost and maximum digest batch size.
//
// The two return values deliberately represent different units:
//
// tokens counts messages. A finding batch produces one message/request, so this is always 1.
// rate_per_min therefore still caps digest messages per minute.
// claimLimit counts findings in a batch, bounded only by memory, not the request budget.
//
// Previously the request budget (notifyMaxSendsPerChannelPerTick,
// derived from the lease) was reused as digest batch size to enforce rate_per_min.
// At 20 messages/minute, a three-second tick replenishes only one token,
// producing one-finding digests that behave like realtime notifications
// with a digest heading; db.MaxDigestBatchSize could never be reached.
//
// End-to-end tests could miss this because they passed a large limit directly
// to stepDigest, bypassing step's token calculation. This decision is pinned by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and sends one message per finding for a channel.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] Failed to claim realtime deliveries channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel type %q is not registered", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// Rendering failures are local data errors; retries cannot repair them.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates a channel's pending deliveries into one message when due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] Failed to determine digest batch readiness channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] Failed to claim digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel type %q is not registered", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Explicitly fail deliveries with unreadable snapshots that cannot enter the message.
	// Otherwise they remain outside included, neither sent nor failed, are missed by
	// batch marking, and stay sending until repeated lease expiry and reclaim.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "Event snapshot cannot be parsed; cannot render this finding as a message"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] Failed to mark delivery with malformed snapshot as failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] Skipped %d deliveries with malformed snapshots channel=%d", len(skipped), ch.ID)
	}
	// Pass only included deliveries to send: included[i] exactly matches msg.Items[i].
	// send uses that correspondence to mark the first K delivered items correctly.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and advances states according to the result.
//
// A batch shares one send result: delivered, or retried together.
// Do not retry individual items within a digest message; it breaks batch semantics.
//
// The exception is splitting at a channel length limit: if only the first K fit,
// entries K+1 onward must remain for the next batch instead of being marked sent.
// Otherwise truncated findings disappear from both the message and the failure list.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Bound each send so one stalled channel cannot block all remaining channels this tick.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// Reported deliveries cannot exceed the batch size; if they do, rendering is wrong.
			// Log and treat all as delivered to avoid corrupting row state.
			log.Printf("[notify] Channel reported %d delivered items for %d deliveries channel=%s; treating all as delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] Failed to mark deliveries as sent channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// Channel length limit reached: return the remainder immediately for the next tick.
			// Use DeferDeliveries rather than RescheduleDeliveries, since this is not a failure
			// and must not consume retries; undo the optimistic attempt increment from claiming.
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("Message reached channel length limit; delivered the first %d items, with the remaining items deferred to the next batch", delivered)); err != nil {
				log.Printf("[notify] Failed to queue remaining digest segments channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// No error and no delivered count is still a failure; use backoff
		// so the delivery is not repeatedly claimed without ever being resolved.
		err = fmt.Errorf("Channel did not report a delivered count (delivered=%d)", delivered)
	}

	// Decide failure handling individually, rather than by the maximum attempts of the batch.
	//
	// Previously maxAttempts(deliveries) >= MaxNotifyAttempts killed the whole batch.
	// Attempts differ within a batch: an older retry could drag
	// a new delivery with attempts=1 into failed without any retries,
	// violating the requirement that old rows must not exhaust new rows' budget.
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] Failed to mark failed state channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("Still failed after %d attempts: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] Failed to mark failed state channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Group reschedules by delay; only three backoff tiers exist,
	// avoiding one UPDATE per item (500 round trips for a batch of 500).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] Failed to reschedule deliveries channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] Delivery failed channel=%d kind=%s permanent=%d exhausted=%d retrying=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns entries in all but not keep, comparing pointer identity.
// It identifies items excluded from messages that need explicit handling.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt loads the channel implementation and parses its configuration.
// ok=false means unregistered; fail directly rather than retrying forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On invalid config JSON, pass an empty map so channel Validate reports missing
		// fields, which is more actionable than a JSON parse error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single-finding message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch parses digest snapshots individually; skip only malformed entries
// rather than discarding the entire batch.
//
// included strictly corresponds one-to-one with msg.Items.
// The caller uses the reported first K items to mark the first K deliveries sent.
// Skipping a snapshot without removing its delivery from included would
// shift indexes, marking bad entries sent and good entries undelivered.
// The caller explicitly fails malformed entries; see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// Exclude bad snapshots from both the message and included.
			// The caller explicitly fails them; they must never be counted as delivered.
			log.Printf("[notify] Skipped unparseable snapshot in digest batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("All %d deliveries in digest batch have unparseable snapshots", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders a snapshot, resolving asset names and the finding detail link.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// Asset name lookup failure must not prevent sending; a missing asset line
		// is preferable to a missing notification.
		log.Printf("[notify] Failed to resolve asset names finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// Detail route: web/src/app/(main)/function/findings/detail/page.tsx.
		// It reads the finding ID from the id query parameter.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens removes at most want tokens from a channel and returns the actual count.
//
// One token = one message (HTTP request). Realtime passes the desired message count;
// digest sends one message for the entire batch and passes 1.
//
// Capacity is the per-minute limit, replenished steadily. ratePerMin<=0 disables limiting,
// returning a finite but generous cap to prevent unlimited backlogs stalling a tick.
//
// want is essential: without it the bucket would be drained even when the caller's
// tick cap cannot use all tokens, wasting burst capacity until replenishment.
// Even a tick with no due deliveries would consume tokens.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Replenish by actual elapsed time at ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before rounding: floating point accumulation may produce
	// 0.9999999999 from two half-refills, which int() would truncate to zero.
	// 1e-9 is far below one token and cannot allow a genuinely insufficient balance.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the master switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the public link base URL without trailing slashes.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the configured period or the default when invalid/unset.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses the delivery event snapshot.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("Event snapshot for delivery %d is empty", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("Failed to parse event snapshot for delivery %d: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// Trust the event row's kind; the snapshot may have been written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
