import { useEffect, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { HelpTip } from "./HelpTip";
import { IPReputation } from "./IPReputation";
import { isValidPublicIP, type IPLookupResult } from "./ipLookup";
import { lookupIP } from "./ipLookupClient";
import { ErrorNotice } from "./ui";

export const manualIPLookupKey = (scope: "privacy" | "server") => ["ip-lookup-manual", scope] as const;

export interface ManualIPLookupState {
  data: IPLookupResult | undefined;
  checkedAt: number;
  busy: boolean;
}

// Ручная проверка имеет собственный результат и не меняет измеренный выход браузера или VLESS.
export function IPLookupManual({ token, scope, busy = false, onStateChange }: {
  token: string;
  scope: "privacy" | "server";
  busy?: boolean;
  onStateChange?: (state: ManualIPLookupState) => void;
}) {
  const id = useId();
  const client = useQueryClient();
  const [input, setInput] = useState("");
  const [validationError, setValidationError] = useState<Error | null>(null);
  const key = manualIPLookupKey(scope);
  const run = useQuery<IPLookupResult>({
    queryKey: key,
    queryFn: ({ signal }) => lookupIP(token, input.trim(), signal),
    enabled: false,
    retry: false,
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  });
  useEffect(() => {
    onStateChange?.({ data: run.data, checkedAt: run.dataUpdatedAt, busy: run.isFetching });
  }, [onStateChange, run.data, run.dataUpdatedAt, run.isFetching]);
  return (
    <>
      <section className="card ip-lookup-manual" aria-label="Проверить другой IP">
        <div className="card-heading">
          <div className="diagnostics-title-with-help">
            <h2>Проверить другой IP</h2>
            <HelpTip label="О ручной проверке IP">
              <p>Введите внешний IPv4 или IPv6. Адрес будет проверен в базе IP2Location.io отдельно от вашего текущего подключения.</p>
              <p>Это справочные сведения об адресе. Проверка не подключается к нему и не проверяет доступность интернета через него.</p>
            </HelpTip>
          </div>
        </div>
        <form className="card-body ip-lookup-form" onSubmit={(event) => {
          event.preventDefault();
          if (!isValidPublicIP(input.trim())) {
            setValidationError(new Error("Введите корректный внешний IPv4 или IPv6 без порта и ссылки. Локальные и служебные адреса не проверяются."));
            return;
          }
          setValidationError(null);
          void run.refetch();
        }}>
          <div className="ip-lookup-field">
            <label htmlFor={id}>IP-адрес</label>
            <input id={id} type="text" value={input} onChange={(event) => setInput(event.target.value)}
              placeholder="Например, 8.8.8.8" autoComplete="off" spellCheck={false} maxLength={64}
              disabled={busy || run.isFetching} aria-invalid={!!validationError} />
          </div>
          {run.isFetching ? (
            <button type="button" className="button" onClick={() => { void client.cancelQueries({ queryKey: key }); }}>Остановить проверку IP</button>
          ) : <button type="submit" className="button secondary-button" disabled={busy}>Проверить IP</button>}
          <ErrorNotice error={validationError || run.error} />
          {run.isFetching && <p className="hint" role="status">Запрашиваем сведения об IP…</p>}
        </form>
      </section>
      {run.data && <IPReputation result={run.data} title="Другой IP" description="Введённый адрес; не результат проверки вашего подключения." />}
    </>
  );
}
