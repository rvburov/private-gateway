import { Icon, type IconName } from "./icons";

export type CheckStatus = "ok" | "warn" | "fail" | "skip" | "info";

export interface CheckResult {
  id: string;
  title: string;
  status: CheckStatus;
  detail: string;
  // Только у проверки трафика сервера: адрес, который видят сайты за шлюзом.
  ip?: string;
}

const labels: Record<CheckStatus, string> = {
  ok: "В порядке",
  warn: "Внимание",
  fail: "Проблема",
  skip: "Пропущено",
  info: "Сведения",
};
const marks: Record<CheckStatus, IconName> = {
  ok: "check",
  warn: "alert",
  fail: "close",
  skip: "minus",
  info: "info",
};
const badges: Record<CheckStatus, string> = {
  ok: "success",
  warn: "warning",
  fail: "danger",
  skip: "neutral",
  info: "neutral",
};

export function CheckList({ checks }: { checks: CheckResult[] }) {
  return (
    <ul className="check-list">
      {checks.map((check) => (
        <li key={check.id}>
          <span className={`check-mark ${check.status}`}>
            <Icon name={marks[check.status]} size={16} />
          </span>
          <div>
            <div className="check-head">
              <strong>{check.title}</strong>
              <span className={`badge ${badges[check.status]}`}>{labels[check.status]}</span>
            </div>
            <p>{check.detail}</p>
          </div>
        </li>
      ))}
    </ul>
  );
}

