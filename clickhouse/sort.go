package clickhouse

import "strings"

type Sort struct {
	By  string `json:"by"`
	Dir string `json:"dir"`
}

var (
	traceSortColumns = map[string]string{"date": "Timestamp", "duration": "Duration"}
	logSortColumns   = map[string]string{"date": "Timestamp"}
)

func TraceListOrderBy(s Sort) string {
	return listOrderBy(s, traceSortColumns)
}

func LogListOrderBy(s Sort) string {
	return listOrderBy(s, logSortColumns)
}

func listOrderBy(s Sort, cols map[string]string) string {
	col, ok := cols[strings.ToLower(strings.TrimSpace(s.By))]
	if !ok {
		return ""
	}
	dir := strings.ToLower(strings.TrimSpace(s.Dir))
	if dir != "asc" && dir != "desc" {
		dir = "desc"
	}
	if col == "Timestamp" && dir == "desc" {
		return ""
	}
	return col + " " + strings.ToUpper(dir)
}

func timestampLimitCutoff(orderBy, table, cond, limit string) (extra, order string) {
	if strings.TrimSpace(limit) == "" || limit == "0" {
		if orderBy == "" {
			return "", "Timestamp DESC"
		}
		return "", orderBy
	}
	switch orderBy {
	case "", "Timestamp DESC":
		inner := "SELECT Timestamp FROM " + table + " WHERE " + cond + " ORDER BY Timestamp DESC LIMIT " + limit
		return "Timestamp >= (SELECT min(Timestamp) FROM (" + inner + "))", "Timestamp DESC"
	case "Timestamp ASC":
		inner := "SELECT Timestamp FROM " + table + " WHERE " + cond + " ORDER BY Timestamp ASC LIMIT " + limit
		return "Timestamp <= (SELECT max(Timestamp) FROM (" + inner + "))", "Timestamp ASC"
	default:
		return "", orderBy
	}
}

func logsListSQL(orderBy, cond, limit string) string {
	q := "SELECT ServiceName, Timestamp, multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), Body, TraceId, ResourceAttributes, LogAttributes"
	q += " FROM @@table_otel_logs@@"
	q += " WHERE " + cond
	extra, orderBy := timestampLimitCutoff(orderBy, "@@table_otel_logs@@", cond, limit)
	if extra != "" {
		q += " AND " + extra
	}
	q += " ORDER BY " + orderBy
	q += " LIMIT " + limit
	return q
}
