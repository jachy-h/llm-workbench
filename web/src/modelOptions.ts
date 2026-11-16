import type { SearchSelectOption } from "./components/SearchSelect";
import type { ModelInfo } from "./types";

export function toModelOptions(models: ModelInfo[]): SearchSelectOption[] {
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
