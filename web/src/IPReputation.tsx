import { HelpTip } from "./HelpTip";
import { Icon } from "./icons";
import { ip2LocationDemo, ipLookupFields, ipLookupSource, ipLookupValue, ipUsageName, lookupStatusLabel, noIPData, type IPLookupResult } from "./ipLookup";

export function IPReputation({ result, title = "IP и репутация", description }: {
  result: IPLookupResult | null;
  title?: string;
  description?: string;
}) {
  const data = result?.data;
  const details = ipLookupFields(data);
  const date = result?.checkedAt ? new Date(result.checkedAt) : null;
  const location = data ? [data.country_name ?? data.country?.name ?? data.country_code, data.city_name ?? data.city?.name]
    .map((value) => ipLookupValue(value)).filter((value) => value !== noIPData).join(", ") : noIPData;
  return (
    <section className="card ip-reputation" aria-label={title}>
      <div className="card-heading">
        <div>
          <div className="diagnostics-title-with-help">
            <h2>{title}</h2>
            <HelpTip label={`О данных «${title}»`}>
              <p>География, тип сети и отметки адреса получены из базы IP2Location.io. Они могут запаздывать или отличаться от сведений других сайтов.</p>
              <p>«Не отмечен в базе» не гарантирует отсутствие VPN или прокси. Адрес дата-центра сам по себе не означает угрозу. Оценка риска от 0 до 99 относится к IP2Location, а не к Claude или другому сайту.</p>
              <p>Проверка IP не определяет точное местоположение человека и не заменяет проверку DNS, IPv6 и WebRTC.</p>
            </HelpTip>
          </div>
          {description && <p>{description}</p>}
        </div>
      </div>
      <div className="card-body">
        {!result ? <p className="hint">Сведения об IP ещё не запрошены.</p> : (
          <>
            <p className="ip-reputation-source hint">{ipLookupSource} · {lookupStatusLabel(result.status)}{date && Number.isFinite(date.getTime()) ? ` · ${date.toLocaleString("ru-RU")}` : ""}</p>
            {data ? (
              <dl className="ip-reputation-summary">
                <div><dt>IP-адрес</dt><dd>{ipLookupValue(result.ip)}</dd></div>
                <div><dt>Страна и город</dt><dd>{location || noIPData}</dd></div>
                <div><dt>Провайдер</dt><dd>{ipLookupValue(data.isp)}</dd></div>
                <div><dt>ASN</dt><dd>{ipLookupValue(data.asn ?? data.as_info?.as_number)}</dd></div>
                <div><dt>Тип использования IP</dt><dd>{ipUsageName(data.usage_type ?? data.as_info?.as_usage_type)}</dd></div>
                <div><dt>VPN</dt><dd>{ipLookupValue(data.proxy?.is_vpn, true)}</dd></div>
                <div><dt>Прокси</dt><dd>{ipLookupValue(data.is_proxy, true)}</dd></div>
                <div><dt>Оценка риска IP2Location</dt><dd>{ipLookupValue(data.fraud_score)}</dd></div>
              </dl>
            ) : <><dl className="ip-reputation-summary"><div><dt>Запрошенный IP-адрес</dt><dd>{ipLookupValue(result.ip)}</dd></div></dl><p>{result.detail || "Сведения об адресе сейчас недоступны."}</p></>}
            {data && result.detail && result.status !== "available" && <p className="hint">{result.detail}</p>}
            {details.length > 0 && (
              <details className="ip-reputation-details">
                <summary>Все сведения об IP</summary>
                <dl>{details.map((field) => <div key={field.label}><dt>{field.label}</dt><dd>{field.value}</dd></div>)}</dl>
              </details>
            )}
            <div className="button-group">
              <a className="button secondary-button" href={ip2LocationDemo} target="_blank" rel="noopener noreferrer">
                Проверить на IP2Location <Icon name="external" size={14} />
              </a>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
