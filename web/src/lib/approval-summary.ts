export function approvalToolName(summary: string): string {
  const match = /Tool\s+(\S+)\s+requests approval/.exec(summary) ?? /\u5de5\u5177\s+(\S+)\s+\u8bf7\u6c42/.exec(summary);
  return match ? match[1] : summary;
}
