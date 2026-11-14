// @ts-check
// Example Crewbase plugin written in plain ESM JavaScript with JSDoc types,
// so it runs on node without a build step. `tsc --checkJs` type-checks it
// against @crewbase/plugin-sdk (provided by the host at runtime).
import { definePlugin } from "@crewbase/plugin-sdk";

const LABEL = "words";

/** @param {string} text */
function count(text) {
  const words = text.split(/\s+/).filter(Boolean).length;
  const lines = text === "" ? 0 : text.split("\n").length;
  return { words, lines, chars: [...text].length };
}

definePlugin({
  name: "word-count",
  version: "0.1.0",
  tools: {
    word_count: {
      description: "Count the words, lines and characters in a text.",
      category: "read",
      params: {
        type: "object",
        properties: { text: { type: "string", description: "Text to count" } },
        required: ["text"],
      },
      /** @param {{ text: string }} args */
      execute(args) {
        const c = count(String(args.text ?? ""));
        return { content: `${LABEL}: ${c.words}, lines: ${c.lines}, chars: ${c.chars}`, data: c };
      },
    },
  },
  onToolResult(exe, host) {
    host.log(`tool ${exe.call.name} finished${exe.result.isError ? " with an error" : ""}`);
  },
});
