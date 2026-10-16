import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const setupPath = "../app/(auth)/setup/page.tsx";
const dialogPath = "../app/(main)/_components/sidebar/change-password-dialog.tsx";

function sourceFile(relativePath) {
  const path = new URL(relativePath, import.meta.url);
  return ts.createSourceFile(
    path.pathname,
    readFileSync(path, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    path.pathname.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
}

function nodes(ast, predicate) {
  const found = [];
  function visit(node) {
    if (predicate(node)) found.push(node);
    ts.forEachChild(node, visit);
  }
  visit(ast);
  return found;
}

function evaluate(source, context = {}) {
  const script = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return vm.runInNewContext(script, context);
}

function apiMethod(name) {
  const ast = sourceFile("../lib/api.ts");
  const api = nodes(ast, (node) => ts.isVariableDeclaration(node) && node.name.getText(ast) === "api")[0];
  const property = api.initializer.properties.find((node) => node.name?.getText(ast) === name);
  assert.ok(property, `api.${name} is missing`);
  return { ast, initializer: property.initializer };
}

function responseFields(name) {
  const { ast, initializer } = apiMethod(name);
  const type = initializer.body.typeArguments[0];
  return type.members.map((member) => [member.name.getText(ast), member.type.getText(ast)]);
}

function loadAuth(storage, document = { cookie: "" }) {
  const ast = sourceFile("../lib/auth.ts");
  const declarations = ast.statements
    .filter((statement) => ts.isVariableStatement(statement))
    .map((statement) => statement.getText(ast).replace(/^export\s+/, ""));
  return evaluate(`${declarations.join("\n")}\nauth;`, {
    localStorage: storage,
    document,
    process: { env: {} },
    window: {},
  });
}

function handler(path, name, context) {
  const ast = sourceFile(path);
  const declaration = nodes(ast, (node) => ts.isFunctionDeclaration(node) && node.name?.text === name)[0];
  assert.ok(declaration, `${name} is missing`);
  return evaluate(`${declaration.getText(ast).replace(/^export\s+/, "")}\n${name};`, context);
}

function setupContext(setupToken) {
  const events = [];
  return {
    events,
    setupToken,
    password: "operator-password",
    confirm: "operator-password",
    setError: (message) => events.push(["error", message]),
    setLoading: (loading) => events.push(["loading", loading]),
    api: {
      initPassword: async (...args) => {
        events.push(["init", ...args]);
        return { token: "fresh-jwt" };
      },
    },
    auth: { setToken: (token) => events.push(["store", token]) },
    router: { replace: (path) => events.push(["route", path]) },
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

// Drain promise callbacks without a clock delay, including callbacks across VM contexts.
const nextTurn = () => new Promise((resolve) => setImmediate(resolve));

function rotationWorkflow({ storageFails = false, invalidToken = false, callbackThrows = false } = {}) {
  const events = [];
  const stored = new Map([["artex_token", "old-jwt"]]);
  const storage = {
    getItem: (key) => stored.get(key) ?? null,
    setItem: (key, value) => {
      if (storageFails) throw new Error("storage denied");
      stored.set(key, value);
    },
    removeItem: (key) => {
      events.push(["remove", key]);
      if (storageFails) throw new Error("removal denied");
      stored.delete(key);
    },
  };
  const document = { cookie: "artex_token=old-jwt" };
  const window = {
    location: {
      href: "/function/tasks",
      assign(path) {
        this.href = path;
        events.push(["redirect", path]);
      },
    },
  };
  const auth = loadAuth(storage, document);
  const requests = [];
  const ast = sourceFile("../lib/api.ts");
  const declarations = ast.statements
    .filter((node) => !ts.isImportDeclaration(node))
    .map((node) => node.getText(ast).replace(/^export\s+/, ""));
  const { api, http } = evaluate(`${declarations.join("\n")}\n({ api, http });`, {
    auth,
    MOCK: false,
    window,
    localStorage: storage,
    fetch: (path, init) => {
      const response = deferred();
      requests.push({ path, init, ...response });
      return response.promise;
    },
  });
  const submit = handler(dialogPath, "submit", {
    auth,
    api,
    window,
    oldPassword: "old",
    newPassword: "new-password",
    confirmPassword: "new-password",
    setSaving: (value) => events.push(["saving", value]),
    toast: {
      success: (message) => events.push(["success", message]),
      error: (message) => events.push(["error", message]),
    },
    onOpenChange: (value) => {
      if (callbackThrows) throw new Error("close failed");
      events.push(["close", value]);
    },
  });
  return {
    auth,
    http,
    events,
    stored,
    document,
    window,
    requests,
    submit: () => submit({ preventDefault: () => undefined }),
    success: (request) =>
      request.resolve({
        status: 200,
        ok: true,
        json: async () => ({ ok: true, token: invalidToken ? "" : "fresh-jwt" }),
      }),
  };
}

test("poll 401 received before password-change success waits for replacement before deciding logout", async () => {
  const flow = rotationWorkflow();
  let settled = false;
  const poll = flow.http("/tasks").catch((error) => {
    settled = true;
    return error;
  });
  flow.submit();
  flow.requests[0].resolve({ status: 401, ok: false });
  await nextTurn();
  assert.equal(flow.window.location.href, "/function/tasks", "poll must not redirect while rotation is pending");
  assert.equal(settled, false);
  flow.success(flow.requests[1]);
  await nextTurn();
  assert.equal(settled, true, "rotation completion must release the poll");
  assert.match((await poll).message, /Unauthorized/);
  assert.equal(flow.stored.get("artex_token"), "fresh-jwt");
  assert.equal(flow.window.location.href, "/function/tasks");
  assert.equal(
    flow.events.some(([kind]) => kind === "success"),
    true,
  );
});

test("poll 401 waits for a failed password change then expires the still-current token", async () => {
  const flow = rotationWorkflow();
  let settled = false;
  const poll = flow.http("/tasks").catch((error) => {
    settled = true;
    return error;
  });
  flow.submit();
  flow.requests[0].resolve({ status: 401, ok: false });
  await nextTurn();
  assert.equal(flow.window.location.href, "/function/tasks");
  assert.equal(settled, false);
  flow.requests[1].reject(new Error("offline"));
  await nextTurn();
  assert.equal(settled, true, "failed rotation must release the poll");
  assert.match((await poll).message, /Unauthorized/);
  assert.equal(flow.stored.has("artex_token"), false);
  assert.equal(flow.window.location.href, "/login");
  assert.equal(
    flow.events.some(([kind, message]) => kind === "error" && message.includes("offline")),
    true,
  );
});

test("password endpoint 401 surfaces its error without waiting on itself or logging out", async () => {
  const flow = rotationWorkflow();
  flow.submit();
  flow.requests[0].resolve({ status: 401, ok: false, json: async () => ({ error: "Current password is incorrect" }) });
  await nextTurn();
  assert.equal(
    flow.events.some(([kind, value]) => kind === "saving" && value === false),
    true,
    "password request must settle rather than wait for its own rotation",
  );
  assert.equal(flow.window.location.href, "/function/tasks");
  assert.equal(flow.stored.get("artex_token"), "old-jwt");
  assert.equal(
    flow.events.some(([kind, message]) => kind === "error" && message.includes("Current password is incorrect")),
    true,
  );
  const poll = flow.http("/tasks").catch((error) => error);
  flow.requests[1].resolve({ status: 401, ok: false });
  await nextTurn();
  assert.equal(flow.window.location.href, "/login", "endpoint failure must release future poll handling");
  assert.match((await poll).message, /Unauthorized/);
});

for (const failure of ["storage", "invalid token", "callback"]) {
  test(`password rotation releases an earlier 401 after ${failure} failure`, async () => {
    const flow = rotationWorkflow({
      storageFails: failure === "storage",
      invalidToken: failure === "invalid token",
      callbackThrows: failure === "callback",
    });
    let settled = false;
    const poll = flow.http("/tasks").catch((error) => {
      settled = true;
      return error;
    });
    flow.submit();
    flow.requests[0].resolve({ status: 401, ok: false });
    await nextTurn();
    assert.equal(settled, false, "poll must wait until the password response is processed");
    flow.success(flow.requests[1]);
    await nextTurn();
    assert.equal(settled, true, "all failure paths must release rotation waiters");
    assert.match((await poll).message, /Unauthorized/);
    assert.equal(flow.window.location.href, failure === "callback" ? "/function/tasks" : "/login");
    assert.equal(
      flow.events.some(([kind, value]) => kind === "saving" && value === false),
      true,
    );
    if (failure !== "callback") {
      assert.equal(
        flow.events.some(([kind]) => kind === "success" || kind === "close"),
        false,
      );
      assert.equal(
        flow.events.some(([kind]) => kind === "remove"),
        true,
      );
      assert.match(flow.document.cookie, /max-age=0/);
    }
  });
}

test("overlapping password rotations keep poll handling pending until both finish", async () => {
  const flow = rotationWorkflow();
  let settled = false;
  const poll = flow.http("/tasks").catch((error) => {
    settled = true;
    return error;
  });
  flow.submit();
  flow.submit();
  assert.equal(flow.requests.length, 3);
  flow.requests[0].resolve({ status: 401, ok: false });
  flow.requests[1].reject(new Error("first request failed"));
  await nextTurn();
  assert.equal(settled, false, "one completed rotation must not release another rotation's waiters");
  assert.equal(flow.window.location.href, "/function/tasks");
  flow.success(flow.requests[2]);
  await nextTurn();
  assert.equal(settled, true);
  assert.match((await poll).message, /Unauthorized/);
  assert.equal(flow.stored.get("artex_token"), "fresh-jwt");
  assert.equal(flow.window.location.href, "/function/tasks");
});

test("auth API response types include setup status and the fresh password-change token", () => {
  assert.deepEqual(responseFields("authStatus"), [
    ["initialized", "boolean"],
    ["setup_token_required", "boolean"],
  ]);
  assert.deepEqual(responseFields("initPassword"), [["token", "string"]]);
  assert.deepEqual(responseFields("changePassword"), [
    ["ok", "boolean"],
    ["token", "string"],
  ]);
});

test("initialization sends exactly the password and setup token", async () => {
  const { ast, initializer } = apiMethod("initPassword");
  const calls = [];
  const initPassword = evaluate(`(${initializer.getText(ast)})`, {
    post: async (path, body) => calls.push([path, JSON.parse(JSON.stringify(body))]),
  });
  await initPassword("operator-password", "console-token");
  assert.deepEqual(calls, [["/auth/init", { password: "operator-password", setup_token: "console-token" }]]);
});

test("password change sends the existing password fields and returns the fresh token", async () => {
  const { ast, initializer } = apiMethod("changePassword");
  const replacement = { ok: true, token: "fresh-jwt" };
  const changePassword = evaluate(`(${initializer.getText(ast)})`, {
    post: async (path, body) => {
      assert.equal(path, "/auth/change-password");
      assert.deepEqual(JSON.parse(JSON.stringify(body)), { old_password: "old", new_password: "new-password" });
      return replacement;
    },
  });
  assert.equal(await changePassword("old", "new-password"), replacement);
});

test("mock status consistently reports that the initialized demo needs no setup token", () => {
  const route = handler("../lib/mock/handler.ts", "route", {});
  const status = route("GET", "/auth/status", [], new URLSearchParams(), {});
  assert.deepEqual(JSON.parse(JSON.stringify(status)), { initialized: true, setup_token_required: false });
});

test("mock initialization accepts the new password and setup-token body", async () => {
  const route = handler("../lib/mock/handler.ts", "route", {});
  const { ast, initializer } = apiMethod("initPassword");
  const initPassword = evaluate(`(${initializer.getText(ast)})`, {
    post: async (path, body) => route("POST", path, [], new URLSearchParams(), body),
  });
  assert.equal((await initPassword("operator-password", "console-token")).token, "mock-demo");
});

test("mock password change supplies a usable replacement token", () => {
  const route = handler("../lib/mock/handler.ts", "route", {});
  const result = route("POST", "/auth/change-password", [], new URLSearchParams(), {
    old_password: "old",
    new_password: "new-password",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(result)), { ok: true, token: "mock-demo" });
  const stored = new Map([["artex_token", "old-demo"]]);
  const auth = loadAuth({ setItem: (key, value) => stored.set(key, value) });
  assert.equal(auth.replaceToken(result.token), true);
  assert.equal(stored.get("artex_token"), "mock-demo");
});

test("replacement stores the fresh token in local storage and the cookie", () => {
  const stored = new Map([["artex_token", "old-jwt"]]);
  const document = { cookie: "artex_token=old-jwt" };
  const auth = loadAuth({ setItem: (key, value) => stored.set(key, value) }, document);
  assert.equal(typeof auth.replaceToken, "function");
  assert.equal(auth.replaceToken("fresh-jwt"), true);
  assert.equal(stored.get("artex_token"), "fresh-jwt");
  assert.match(document.cookie, /^artex_token=fresh-jwt;/);
});

test("failed replacement removes the old token and expires the cookie", () => {
  const stored = new Map([["artex_token", "old-jwt"]]);
  const document = { cookie: "artex_token=old-jwt" };
  const auth = loadAuth(
    {
      setItem: () => {
        throw new Error("storage denied");
      },
      removeItem: (key) => stored.delete(key),
    },
    document,
  );
  assert.equal(typeof auth.replaceToken, "function");
  assert.equal(auth.replaceToken("fresh-jwt"), false);
  assert.equal(stored.has("artex_token"), false);
  assert.match(document.cookie, /^artex_token=;.*max-age=0/);
});

test("cookie failure after a partial replacement clears the stored token", () => {
  const stored = new Map([["artex_token", "old-jwt"]]);
  const writes = [];
  const auth = loadAuth(
    {
      setItem: (key, value) => {
        stored.set(key, value);
        writes.push(value);
      },
      removeItem: (key) => stored.delete(key),
    },
    {
      set cookie(value) {
        writes.push(value);
        throw new Error("cookies denied");
      },
    },
  );
  assert.equal(typeof auth.replaceToken, "function");
  assert.equal(auth.replaceToken("fresh-jwt"), false);
  assert.equal(writes[0], "fresh-jwt");
  assert.equal(stored.has("artex_token"), false);
  assert.match(writes[2], /^artex_token=;.*max-age=0/);
});

test("replacement reports failure even when clearing storage also throws", () => {
  let removals = 0;
  const auth = loadAuth({
    setItem: () => {
      throw new Error("storage denied");
    },
    removeItem: () => {
      removals++;
      throw new Error("storage denied");
    },
  });
  assert.equal(typeof auth.replaceToken, "function");
  assert.equal(auth.replaceToken("fresh-jwt"), false);
  assert.equal(removals, 1);
});

test("clearing auth expires the cookie even when local storage removal throws", () => {
  const cookies = [];
  const auth = loadAuth(
    {
      removeItem: () => {
        throw new Error("removal denied");
      },
    },
    {
      set cookie(value) {
        cookies.push(value);
      },
    },
  );
  assert.throws(() => auth.clearToken(), /removal denied/);
  assert.deepEqual(cookies, ["artex_token=; path=/; max-age=0"]);
});

test("partial replacement failure attempts cookie expiration even when removal throws", () => {
  const events = [];
  const auth = loadAuth(
    {
      setItem: (key, value) => events.push(["store", key, value]),
      removeItem: (key) => {
        events.push(["remove", key]);
        throw new Error("removal denied");
      },
    },
    {
      set cookie(value) {
        events.push(["cookie", value]);
        if (value.includes("fresh-jwt")) throw new Error("replacement cookie denied");
      },
    },
  );
  assert.equal(auth.replaceToken("fresh-jwt"), false);
  assert.deepEqual(
    events.map((event) => event[0]),
    ["store", "cookie", "remove", "cookie"],
  );
  assert.equal(events[3][1], "artex_token=; path=/; max-age=0");
});

for (const token of ["", undefined, null, "   "]) {
  test(`invalid replacement token ${JSON.stringify(token)} is never stored and clears old auth`, () => {
    const stored = new Map([["artex_token", "old-jwt"]]);
    const writes = [];
    const document = { cookie: "artex_token=old-jwt" };
    const auth = loadAuth(
      {
        setItem: (key, value) => {
          writes.push(value);
          stored.set(key, value);
        },
        removeItem: (key) => stored.delete(key),
      },
      document,
    );
    assert.equal(auth.replaceToken(token), false);
    assert.deepEqual(writes, []);
    assert.equal(stored.has("artex_token"), false);
    assert.equal(document.cookie, "artex_token=; path=/; max-age=0");
  });
}

for (const [initialToken, replacement] of [
  ["old-jwt", "fresh-jwt"],
  [null, "fresh-jwt"],
  ["old-jwt", null],
  [null, null],
]) {
  test(`delayed ${initialToken ? "authenticated" : "unauthenticated"} 401 ${replacement ? "preserves a newer session" : "clears the current session and redirects"}`, async () => {
    const stored = new Map(initialToken ? [["artex_token", initialToken]] : []);
    const storage = {
      getItem: (key) => stored.get(key) ?? null,
      setItem: (key, value) => stored.set(key, value),
      removeItem: (key) => stored.delete(key),
    };
    const document = { cookie: initialToken ? `artex_token=${initialToken}` : "" };
    const window = { location: { href: "/function/tasks" } };
    const auth = loadAuth(storage, document);
    let respond;
    let request;
    const http = handler("../lib/api.ts", "http", {
      MOCK: false,
      getToken: handler("../lib/api.ts", "getToken", { localStorage: storage, window }),
      auth,
      localStorage: storage,
      document,
      window,
      fetch: (path, init) => {
        request = { path, init };
        return new Promise((resolve) => {
          respond = resolve;
        });
      },
    });
    const pending = http("/tasks");
    assert.equal(request.path, "/api/tasks");
    assert.equal(request.init.headers.Authorization, initialToken ? `Bearer ${initialToken}` : undefined);
    if (replacement) assert.equal(auth.replaceToken(replacement), true);
    respond({ status: 401, ok: false });
    await assert.rejects(pending, /Unauthorized/);
    assert.equal(stored.get("artex_token") ?? null, replacement);
    assert.equal(window.location.href, replacement ? "/function/tasks" : "/login");
    if (replacement) assert.match(document.cookie, /^artex_token=fresh-jwt;/);
    else assert.equal(document.cookie, "artex_token=; path=/; max-age=0");
  });
}

test("current-token 401 still expires cookies and redirects when storage removal throws", async () => {
  const cookies = [];
  const storage = {
    getItem: () => "old-jwt",
    removeItem: () => {
      throw new Error("removal denied");
    },
  };
  const document = {
    set cookie(value) {
      cookies.push(value);
    },
  };
  const window = { location: { href: "/function/tasks" } };
  const http = handler("../lib/api.ts", "http", {
    MOCK: false,
    getToken: handler("../lib/api.ts", "getToken", { localStorage: storage, window }),
    auth: loadAuth(storage, document),
    localStorage: storage,
    document,
    window,
    fetch: async () => ({ status: 401, ok: false }),
  });
  await assert.rejects(http("/tasks"), /Unauthorized/);
  assert.deepEqual(cookies, ["artex_token=; path=/; max-age=0"]);
  assert.equal(window.location.href, "/login");
});

test("setup rejects a blank token before calling initialization", async () => {
  const context = setupContext("   ");
  await handler(setupPath, "handleSubmit", context)({ preventDefault() {} });
  assert.deepEqual(context.events, [["error", "Enter the setup token from the server console or deployment secret"]]);
});

test("setup trims the token and preserves sign-in routing", async () => {
  const context = setupContext("  console-token  ");
  await handler(setupPath, "handleSubmit", context)({ preventDefault() {} });
  assert.deepEqual(context.events, [
    ["loading", true],
    ["error", ""],
    ["init", "operator-password", "console-token"],
    ["store", "fresh-jwt"],
    ["route", "/function/tasks"],
    ["loading", false],
  ]);
});

test("setup stays fail-closed while checking status and when status is unavailable", async () => {
  const ast = sourceFile(setupPath);
  const checking = nodes(ast, (node) => ts.isIfStatement(node) && node.expression.getText(ast) === "checking")[0];
  assert.ok(checking && !checking.thenStatement.getText(ast).includes("<form"));
  const unavailable = nodes(
    ast,
    (node) => ts.isConditionalExpression(node) && node.condition.getText(ast) === "unavailable",
  ).find((node) => node.whenFalse.getText(ast).includes("<form"));
  assert.ok(unavailable);
  assert.equal(unavailable.whenTrue.getText(ast).includes("<form"), false);
  const effect = nodes(ast, (node) => ts.isCallExpression(node) && node.expression.getText(ast) === "useEffect")[0];
  let unavailableState = false;
  let checkingState = true;
  const routes = [];
  const context = {
    api: {
      authStatus: async () => {
        throw new Error("offline");
      },
    },
    router: { replace: (path) => routes.push(path) },
    setError() {},
    setUnavailable: (value) => {
      unavailableState = value;
    },
    setChecking: (value) => {
      checkingState = value;
    },
  };
  evaluate(`(${effect.arguments[0].getText(ast)})`, context)();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(unavailableState, true);
  assert.equal(checkingState, false);
  assert.deepEqual(routes, []);
});

test("setup token field is accessible, required, private, first, and gates submission", () => {
  const ast = sourceFile(setupPath);
  const inputs = nodes(ast, (node) => ts.isJsxSelfClosingElement(node) && node.tagName.getText(ast) === "Input");
  const attributes = (node) =>
    new Map(node.attributes.properties.map((attribute) => [attribute.name.getText(ast), attribute.initializer]));
  const token = inputs.find((node) => attributes(node).get("id")?.text === "setup-token");
  assert.ok(token, "setup token field is missing");
  assert.equal(inputs[0], token);
  const attrs = attributes(token);
  assert.equal(attrs.get("type").text, "password");
  assert.equal(attrs.get("autoComplete").text, "off");
  assert.equal(attrs.get("spellCheck").expression.kind, ts.SyntaxKind.FalseKeyword);
  assert.ok(attrs.has("required"));
  assert.ok(attrs.has("autoFocus"));
  assert.equal(
    inputs.slice(1).some((node) => attributes(node).has("autoFocus")),
    false,
  );
  assert.ok(
    nodes(
      ast,
      (node) =>
        ts.isJsxAttribute(node) && node.name.getText(ast) === "htmlFor" && node.initializer?.text === "setup-token",
    ).length,
  );
  const submit = nodes(ast, (node) => ts.isJsxOpeningElement(node) && node.tagName.getText(ast) === "Button").find(
    (node) => attributes(node).get("type")?.text === "submit",
  );
  const disabled = attributes(submit).get("disabled").expression.getText(ast);
  for (const [setupToken, password, confirm, expected] of [
    ["  ", "password", "password", true],
    ["token", "", "password", true],
    ["token", "password", "", true],
    ["token", "password", "password", false],
  ]) {
    assert.equal(evaluate(disabled, { loading: false, setupToken, password, confirm }), expected);
  }
  assert.match(ast.text, /docker compose logs artex/);
  assert.match(ast.text, /configured setup token/);
});

for (const replacementSucceeds of [true, false]) {
  test(`password dialog ${replacementSucceeds ? "stores the fresh token before success and closing" : "redirects after storage failure without success or closing"}`, async () => {
    const events = [];
    let pending;
    const context = {
      oldPassword: "old",
      newPassword: "new-password",
      confirmPassword: "new-password",
      setSaving: (saving) => events.push(["saving", saving]),
      api: {
        changePassword: (...args) => {
          events.push(["change", ...args]);
          pending = Promise.resolve({ ok: true, token: "fresh-jwt" });
          return pending;
        },
      },
      auth: {
        beginTokenRotation: () => () => undefined,
        replaceToken: (token) => {
          events.push(["replace", token]);
          return replacementSucceeds;
        },
      },
      toast: {
        success: (message) => events.push(["success", message]),
        error: (message) => events.push(["error", message]),
      },
      onOpenChange: (open) => events.push(["close", open]),
      window: { location: { assign: (path) => events.push(["redirect", path]) } },
    };
    handler(dialogPath, "submit", context)({ preventDefault() {} });
    await pending;
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(events.slice(0, 3), [
      ["saving", true],
      ["change", "old", "new-password"],
      ["replace", "fresh-jwt"],
    ]);
    assert.deepEqual(
      events.slice(3),
      replacementSucceeds
        ? [
            ["success", "Password changed; other sessions are signed out"],
            ["close", false],
            ["saving", false],
          ]
        : [
            ["error", "Password changed, but the new session could not be stored. Sign in again."],
            ["redirect", "/login"],
            ["saving", false],
          ],
    );
  });
}

test("password dialog explains session revocation and this browser's fresh token", () => {
  const ast = sourceFile(dialogPath);
  const description = nodes(
    ast,
    (node) => ts.isJsxElement(node) && node.openingElement.tagName.getText(ast) === "DialogDescription",
  )[0];
  assert.match(description.getText(ast), /signs out every other session/);
  assert.match(description.getText(ast), /This browser stores a fresh sign-in token/);
});
