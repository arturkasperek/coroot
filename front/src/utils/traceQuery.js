const TRACE_FIELD_LABELS = {
    Source: 'Source',
    Status: 'Status',
    Server: 'Server',
    Client: 'Client',
    Namespace: 'Namespace',
    ServiceName: 'Application',
    ApiRoute: 'API Route',
    SpanName: 'Root Span Name',
    TraceId: 'Trace ID',
};

const TRACE_FIELDS_BY_LABEL = Object.fromEntries(Object.entries(TRACE_FIELD_LABELS).map(([field, label]) => [label, field]));

export const TRACE_QUERY_FIELDS = [...Object.values(TRACE_FIELD_LABELS), 'Sort'];

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

// The status classes of the requests chart, in its order (ch.TraceStatusClasses).
export const TRACE_STATUS_CLASSES = ['2xx', '3xx', '4xx', '5xx', 'error', 'other'];

// The palette colors of the status classes, in the requests chart and the Status facet.
export const TRACE_STATUS_COLORS = {
    '2xx': 'green-lighten1',
    '3xx': 'blue-lighten2',
    '4xx': 'orange-lighten1',
    '5xx': 'red-lighten1',
    error: 'red-darken4',
    other: 'grey-lighten1',
};

// The palette colors of the latency chart: p95 lighter, behind p50.
export const TRACE_LATENCY_COLORS = {
    p95: 'blue-lighten3',
    p50: 'blue-darken2',
};

// withSeriesColors gives the series of a chart the palette colors of their names.
export function withSeriesColors(chart, colors) {
    if (!chart) {
        return chart;
    }
    return { ...chart, series: (chart.series || []).map((series) => ({ ...series, color: colors[series.name] || series.color })) };
}

function formatDuration(seconds) {
    const value = Number(seconds);
    if (value < 1) {
        return `${Math.round(value * 1000)} ms`;
    }
    return `${Number(value.toFixed(2))} s`;
}

function durationChipLabel(from, to) {
    // a selection of the errors row of the former heatmap, kept for old links
    if (from === 'inf' || to === 'err') {
        return 'Errors';
    }
    if (from && to) {
        return `Duration ${formatDuration(from)}–${formatDuration(to)}`;
    }
    if (from) {
        return `Duration ≥ ${formatDuration(from)}`;
    }
    return `Duration ≤ ${formatDuration(to)}`;
}

// The chips of the selection made on the charts, one per part: the time range
// (of links made before a drag changed the time window), the duration range.
// formatTime formats a timestamp in ms.
export function selectionChips(query, formatTime) {
    const chips = [];
    if (query.ts_from || query.ts_to) {
        const from = query.ts_from ? formatTime(query.ts_from) : '…';
        const to = query.ts_to ? formatTime(query.ts_to) : '…';
        chips.push({ key: 'time', label: `Time ${from}–${to}` });
    }
    if (query.dur_from || query.dur_to) {
        chips.push({ key: 'duration', label: durationChipLabel(query.dur_from, query.dur_to) });
    }
    return chips;
}

const SELECTION_FIELDS = {
    time: ['ts_from', 'ts_to'],
    duration: ['dur_from', 'dur_to'],
};

// The query without one part of the selection, a key of a selection chip.
export function removeSelection(query, key) {
    const result = { ...query };
    (SELECTION_FIELDS[key] || []).forEach((field) => {
        result[field] = undefined;
    });
    return result;
}

// The filters with a status class added to the classes shown (the values of
// one field are alternatives), or removed when it is already there.
export function toggleStatusFilter(filters = [], status) {
    if (!TRACE_STATUS_CLASSES.includes(status)) {
        return filters;
    }
    const isThisStatus = (filter) => filter.field === 'Status' && filter.op === '=' && filter.value === status;
    if (filters.some(isThisStatus)) {
        return filters.filter((filter) => !isThisStatus(filter));
    }
    return [...filters, { field: 'Status', op: '=', value: status }];
}

// The query narrowed to the requests slower than seconds (the p95 of the view),
// or no longer narrowed by duration when it already is.
export function toggleSlowerThanSelection(query, seconds) {
    const durFrom = String(Math.round(seconds * 1000) / 1000);
    if (query.dur_from === durFrom && !query.dur_to) {
        return { ...query, dur_from: undefined };
    }
    return { ...query, dur_from: durFrom, dur_to: undefined };
}
