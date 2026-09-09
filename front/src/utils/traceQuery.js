const TRACE_FIELD_LABELS = {
    ServiceName: 'Root Service Name',
    SpanName: 'Root Span Name',
    TraceId: 'Trace ID',
};

const TRACE_FIELDS_BY_LABEL = Object.fromEntries(Object.entries(TRACE_FIELD_LABELS).map(([field, label]) => [label, field]));

export const TRACE_QUERY_FIELDS = Object.values(TRACE_FIELD_LABELS);

export function toQueryBuilderFilters(filters = []) {
    return filters
        .filter((filter) => TRACE_FIELD_LABELS[filter.field])
        .map((filter) => ({
            name: TRACE_FIELD_LABELS[filter.field],
            op: filter.op,
            value: filter.value,
        }));
}

export function fromQueryBuilderFilters(filters = []) {
    return filters
        .filter((filter) => TRACE_FIELDS_BY_LABEL[filter.name])
        .map((filter) => ({
            field: TRACE_FIELDS_BY_LABEL[filter.name],
            op: filter.op,
            value: filter.value,
        }));
}
