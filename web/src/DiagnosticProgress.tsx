export interface DiagnosticStage {
  label: string;
  percent: number;
}

// Процент обозначает завершённые этапы, а не время ожидания или оценку безопасности.
export function DiagnosticProgress({ stage, busy, stopped, failed, complete, label, stoppedLabel }: {
  stage: DiagnosticStage;
  busy: boolean;
  stopped: boolean;
  failed: boolean;
  complete: boolean;
  label: string;
  stoppedLabel: string;
}) {
  if (!busy && !stopped && !failed && !complete) return null;
  const finished = !busy && !stopped && !failed && complete;
  const percent = finished ? 100 : Math.max(0, Math.min(99, Math.floor(stage.percent)));
  const message = busy ? stage.label : stopped ? stoppedLabel : failed ? "Не удалось завершить проверку." : "Проверка завершена.";
  return (
    <div className="check-progress">
      <div className="check-progress-caption" role="status" aria-live="polite">
        <span>{message}</span><span className="check-progress-percent">{percent}%</span>
      </div>
      <progress max={100} value={percent} aria-label={label} aria-valuetext={`${percent}%. ${message}`} />
    </div>
  );
}
