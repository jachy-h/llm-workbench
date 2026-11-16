import { useI18n } from "../i18n";

type Props = {
  prompt?: number;
  completion?: number;
  total?: number;
  className?: string;
};

function TokenInputIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden="true" focusable="false">
      <path
        d="M8 10.5V2.8M5.2 5.6 8 2.8l2.8 2.8M3 13.2h10"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function TokenOutputIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden="true" focusable="false">
      <path
        d="M8 2.8v7.7M5.2 7.7 8 10.5l2.8-2.8M3 13.2h10"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function TokenTotalIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" aria-hidden="true" focusable="false">
      <path
        d="M12 3.5H4.5l4 4.5-4 4.5H12"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function tokenValue(value?: number): string {
  return value == null ? "?" : String(value);
}

export default function TokenUsage({ prompt, completion, total, className }: Props) {
  const { t } = useI18n();

  if (prompt == null && completion == null && total == null) {
    return <span className={`token-usage empty${className ? ` ${className}` : ""}`}>—</span>;
  }

  return (
    <span className={`token-usage${className ? ` ${className}` : ""}`}>
      <span
        className="token-usage-part input"
        title={t("common.tokensInput")}
        aria-label={`${t("common.tokensInput")} ${tokenValue(prompt)}`}
      >
        <TokenInputIcon />
        <span className="token-usage-value">{tokenValue(prompt)}</span>
      </span>
      <span
        className="token-usage-part output"
        title={t("common.tokensOutput")}
        aria-label={`${t("common.tokensOutput")} ${tokenValue(completion)}`}
      >
        <TokenOutputIcon />
        <span className="token-usage-value">{tokenValue(completion)}</span>
      </span>
      {total == null ? null : (
        <span
          className="token-usage-part total"
          title={t("common.tokensTotal")}
          aria-label={`${t("common.tokensTotal")} ${total}`}
        >
          <TokenTotalIcon />
          <span className="token-usage-value">{total}</span>
        </span>
      )}
    </span>
  );
}
