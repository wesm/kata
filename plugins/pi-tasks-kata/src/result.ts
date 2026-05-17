export function textResult(text: string) {
  return {
    content: [{ type: "text" as const, text }],
    details: {},
  };
}
