import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ModelEntry } from "../port/port";
import { Models } from "./Models";

const models = Array.from({ length: 9 }, (_, index): ModelEntry => ({
  ref: `provider/model-${index}`,
  provider: "provider",
  model: `model-${index}`,
  vendor: "api.example.com",
  kind: "openai",
  keyEnv: "EXAMPLE_API_KEY",
}));

describe("model search layout", () => {
  it("does not reuse the sticky popup-menu search class in settings", () => {
    const html = renderToStaticMarkup(
      <Models models={models} busy="" protocol={{}} onPick={() => {}} />,
    );
    expect(html).toContain('class="models-find"');
    expect(html).not.toContain('class="mfind"');
  });
});
