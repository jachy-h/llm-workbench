import type { SelectOption } from "./components/Select";
import type { ModelInfo } from "./types";

export function toModelOptions(models: ModelInfo[]): SelectOption[] {
  return models.map((model) => {
    const hint = [model.parameter_size, model.quantization].filter(Boolean).join(" · ");
    return {
      value: model.id,
      label: model.name || model.id,
      hint: hint || undefined,
      keywords: [model.id, model.family].filter(Boolean).join(" "),
    };
  });
}
