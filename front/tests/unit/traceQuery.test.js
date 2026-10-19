const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadTraceQuery() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/traceQuery.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

async function loadTraceQuickFilters() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/traceQuickFilters.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

async function loadSortFromTraceQuery() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/observabilitySort.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('shows trace fields with user-facing names in the query builder', async () => {
    const { TRACE_QUERY_FIELDS } = await loadTraceQuery();

    assert.deepEqual(TRACE_QUERY_FIELDS, [
        'Source',
        'Status',
        'Server',
        'Client',
        'Namespace',
        'Application',
        'API Route',
        'Root Span Name',
        'Trace ID',
        'Sort',
    ]);
});

test('maps existing trace filters to query-builder filters', async () => {
    const { toQueryBuilderFilters } = await loadTraceQuery();

    assert.deepEqual(
        toQueryBuilderFilters([
            { field: 'Source', op: '=', value: 'agent' },
            { field: 'Namespace', op: '=', value: 'coroot-dev' },
            { field: 'ServiceName', op: '=', value: 'express-demo' },
            { field: 'ApiRoute', op: '=', value: 'GET /health' },
            { field: 'SpanName', op: '~', value: 'GET.*' },
            { field: 'TraceId', op: '=', value: 'abc123' },
        ]),
        [
            { name: 'Source', op: '=', value: 'agent' },
            { name: 'Namespace', op: '=', value: 'coroot-dev' },
            { name: 'Application', op: '=', value: 'express-demo' },
            { name: 'API Route', op: '=', value: 'GET /health' },
            { name: 'Root Span Name', op: '~', value: 'GET.*' },
            { name: 'Trace ID', op: '=', value: 'abc123' },
        ],
    );
});

test('maps query-builder filters back to the trace API schema', async () => {
    const { fromQueryBuilderFilters } = await loadTraceQuery();

    assert.deepEqual(
        fromQueryBuilderFilters([
            { name: 'Source', op: '!=', value: 'otel' },
            { name: 'Namespace', op: '=', value: 'coroot-dev' },
            { name: 'Application', op: '!=', value: 'worker' },
            { name: 'API Route', op: '=', value: 'GET /health' },
            { name: 'Root Span Name', op: '=', value: 'GET /api/hello' },
            { name: 'Trace ID', op: '=', value: 'abc123' },
        ]),
        [
            { field: 'Source', op: '!=', value: 'otel' },
            { field: 'Namespace', op: '=', value: 'coroot-dev' },
            { field: 'ServiceName', op: '!=', value: 'worker' },
            { field: 'ApiRoute', op: '=', value: 'GET /health' },
            { field: 'SpanName', op: '=', value: 'GET /api/hello' },
            { field: 'TraceId', op: '=', value: 'abc123' },
        ],
    );
});

test('maps Sort chip to query.sort and does not send it as a ClickHouse filter', async () => {
    const { fromQueryBuilderFilters } = await loadTraceQuery();
    const { appendSortChip } = await loadSortFromTraceQuery();

    assert.deepEqual(appendSortChip([{ name: 'Application', op: '=', value: 'express-demo' }], { by: 'duration', dir: 'desc' }), [
        { name: 'Application', op: '=', value: 'express-demo' },
        { name: 'Sort', op: '=', value: 'Duration desc' },
    ]);
    assert.deepEqual(
        fromQueryBuilderFilters([
            { name: 'Application', op: '=', value: 'express-demo' },
            { name: 'Sort', op: '=', value: 'Duration desc' },
        ]),
        [{ field: 'ServiceName', op: '=', value: 'express-demo' }],
    );
});

test('ignores unsupported query-builder fields instead of sending invalid trace columns', async () => {
    const { fromQueryBuilderFilters } = await loadTraceQuery();

    assert.deepEqual(fromQueryBuilderFilters([{ name: 'Message', op: 'contains', value: 'boom' }]), []);
});

test('overview logs and traces use the shared query panel', async () => {
    const [logs, traces] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/components/Logs.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
    ]);

    for (const source of [logs, traces]) {
        assert.match(source, /import QueryPanel from ['"]@\/components\/QueryPanel\.vue['"]/);
        assert.match(source, /<QueryPanel\b/);
    }
});

test('logs and trace results use the shared observability table', async () => {
    const [logs, traces, table] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/components/Logs.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/ObservabilityTable.vue'), 'utf8'),
    ]);

    for (const source of [logs, traces]) {
        assert.match(source, /import ObservabilityTable from ['"]@\/components\/ObservabilityTable\.vue['"]/);
        assert.match(source, /<ObservabilityTable\b/);
    }
    assert.match(table, /<v-simple-table\b/);
    assert.match(table, /class="observability-table"/);
    assert.match(table, /class="marker"/);
});

test('shared observability table supports per-row cell classes for trace emphasis', async () => {
    const [traces, table] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/ObservabilityTable.vue'), 'utf8'),
    ]);

    assert.match(table, /cellClassValue\(header, item\)/);
    assert.match(traces, /blue--text text--lighten-2/);
    assert.match(traces, /trace\.status\.error \? 'red--text text--lighten-1' : 'green--text text--lighten-1'/);
});

test('trace details show correlated logs below the trace with a link to the full logs view', async () => {
    const traces = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');
    const tracePosition = traces.indexOf('<TracingTrace');
    const logsLinkPosition = traces.indexOf('View in Logs');
    const logsTablePosition = traces.indexOf(':headers="traceLogHeaders"');

    assert.ok(tracePosition >= 0);
    assert.ok(logsLinkPosition > tracePosition);
    assert.ok(logsTablePosition > logsLinkPosition);
    assert.match(traces, /getTraceLogs\(\)/);
    assert.match(traces, /name: 'TraceId', op: '=', value: this\.query\.trace_id/);
    assert.match(traces, /:items="traceLogEntries"/);
    assert.match(traces, /empty-text="No correlated logs found"/);
});

test('traces puts Query above the charts and hides unsupported controls', async () => {
    const traces = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');

    assert.ok(traces.indexOf('<QueryPanel') < traces.indexOf('<Chart'));
    assert.doesNotMatch(traces, /<Heatmap/);
    assert.doesNotMatch(traces, /Integrate OpenTelemetry/);
    assert.doesNotMatch(traces, /Exclude auxiliary requests/);
    assert.doesNotMatch(traces, /excludeAux/);
    assert.match(traces, /include_aux: true/);
    assert.doesNotMatch(traces, /select a chart area to see traces/);
});

test('traces sidebar exposes namespace, server, client, and span name groups', async () => {
    const traces = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');

    assert.match(traces, /buildTraceQuickFilters/);
    assert.match(traces, /view\.facets/);
    assert.match(traces, /local-search-keys="\['Namespace', 'Server', 'Client', 'ApiRoute', 'SpanName'\]"/);
    assert.match(traces, /case 'Server':/);
    assert.match(traces, /case 'Application':/);
    assert.match(traces, /case 'API Route':/);
    assert.doesNotMatch(traces, /case 'Root Service Name':/);
    assert.doesNotMatch(traces, /Root ID/);
});

test('traces and logs sort Date via Query and column headers; traces also sort Duration', async () => {
    const [traces, logs, table] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/Logs.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/ObservabilityTable.vue'), 'utf8'),
    ]);

    assert.match(table, /@click="onHeaderClick/);
    assert.match(table, /header\.sortable/);
    assert.match(traces, /@sort="sortTraces"/);
    assert.match(traces, /sortable: true/);
    assert.match(traces, /case 'Sort':/);
    assert.match(logs, /@sort="sortLogs"/);
    assert.match(logs, /sortable: true/);
    assert.match(logs, /name === 'Sort'/);
    assert.match(logs, /query\.sort/);
    assert.doesNotMatch(logs, /by: 'duration'/);
});

test('QueryBuilder does not emit input while copying parent value into filters', async () => {
    const qb = await readFile(path.resolve(__dirname, '../../src/components/QueryBuilder.vue'), 'utf8');
    assert.match(qb, /this\._syncingFilters = true/);
    assert.match(qb, /if \(this\._syncingFilters\) \{\s*return;/);
    assert.match(qb, /this\.\$emit\('input', this\.filters\)/);
});

test('the traces query builder labels Source values as trace sources, not log sources', async () => {
    const [traces, panel, qb] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/QueryPanel.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/QueryBuilder.vue'), 'utf8'),
    ]);
    assert.match(traces, /<QueryPanel[^>]*:source-label="displayTraceSourceName"/);
    assert.match(panel, /<QueryBuilder[\s\S]*?:source-label="sourceLabel"/);
    assert.match(qb, /formatLogFilter\(filter, this\.sourceLabel\)/);
    assert.match(qb, /return this\.sourceLabel\(item\)/);
});

test('Logs uses applyBuilderFilters and shouldReloadQuery to break the sort refetch loop', async () => {
    const logs = await readFile(path.resolve(__dirname, '../../src/components/Logs.vue'), 'utf8');
    assert.match(logs, /applyBuilderFilters\(this\.query, filters\)/);
    assert.match(logs, /shouldReloadQuery\(this\.querySerialized, curr\)/);
});

test('a chip for each part of the chart selection', async () => {
    const { selectionChips } = await loadTraceQuery();
    const formatTime = (ms) => `t${ms}`;

    assert.deepEqual(selectionChips({}, formatTime), []);
    assert.deepEqual(
        selectionChips({ ts_from: 1000, ts_to: 2000, dur_from: '0.48', filters: [{ field: 'Status', op: '=', value: '5xx' }] }, formatTime),
        [
            { key: 'time', label: 'Time t1000–t2000' },
            { key: 'duration', label: 'Duration ≥ 480 ms' },
        ],
    );
    assert.deepEqual(selectionChips({ dur_from: '0.25', dur_to: '2.5' }, formatTime), [{ key: 'duration', label: 'Duration 250 ms–2.5 s' }]);
    assert.deepEqual(selectionChips({ dur_to: '1' }, formatTime), [{ key: 'duration', label: 'Duration ≤ 1 s' }]);
    // a link made with the former heatmap's errors row
    assert.deepEqual(selectionChips({ dur_from: 'inf', dur_to: 'err' }, formatTime), [{ key: 'duration', label: 'Errors' }]);
});

test('removing a chip removes its part of the selection only', async () => {
    const { removeSelection } = await loadTraceQuery();
    const query = { view: 'traces', ts_from: 1000, ts_to: 2000, dur_from: '0.48', dur_to: '1' };

    assert.deepEqual(removeSelection(query, 'time'), { ...query, ts_from: undefined, ts_to: undefined });
    assert.deepEqual(removeSelection(query, 'duration'), { ...query, dur_from: undefined, dur_to: undefined });
    assert.equal(query.dur_from, '0.48', 'the query itself is not changed');
});

test('a click on a status class adds it to the Status filters, a second click removes it', async () => {
    const { toggleStatusFilter, toQueryBuilderFilters } = await loadTraceQuery();
    const service = { field: 'ServiceName', op: '=', value: 'flask-demo' };

    assert.deepEqual(toggleStatusFilter(undefined, '5xx'), [{ field: 'Status', op: '=', value: '5xx' }]);
    assert.deepEqual(toggleStatusFilter([service], '5xx'), [service, { field: 'Status', op: '=', value: '5xx' }]);
    // classes add up: the values of one field are alternatives
    const excluded = { field: 'Status', op: '!=', value: 'other' };
    assert.deepEqual(toggleStatusFilter([{ field: 'Status', op: '=', value: '4xx' }, excluded], '5xx'), [
        { field: 'Status', op: '=', value: '4xx' },
        excluded,
        { field: 'Status', op: '=', value: '5xx' },
    ]);
    assert.deepEqual(toggleStatusFilter([service, { field: 'Status', op: '=', value: '5xx' }], '5xx'), [service]);
    assert.deepEqual(toggleStatusFilter([service], 'p50'), [service], 'not a status class');
    // the filter is a chip of the Query bar
    assert.deepEqual(toQueryBuilderFilters(toggleStatusFilter([], '5xx')), [{ name: 'Status', op: '=', value: '5xx' }]);
});

test('a click on p95 selects the slower requests, a second click removes it', async () => {
    const { toggleSlowerThanSelection } = await loadTraceQuery();

    assert.deepEqual(toggleSlowerThanSelection({ dur_to: '1' }, 0.48230001), { dur_from: '0.482', dur_to: undefined });
    assert.deepEqual(toggleSlowerThanSelection({ dur_from: '0.482' }, 0.48230001), { dur_from: undefined });
});

test('the frontend colors the series of the traces charts by name', async () => {
    const { TRACE_STATUS_COLORS, withSeriesColors } = await loadTraceQuery();
    const chart = {
        title: 'Requests',
        series: [
            { name: '2xx', data: [1] },
            { name: 'unknown', data: [2], color: 'black' },
        ],
    };

    assert.deepEqual(withSeriesColors(chart, TRACE_STATUS_COLORS), {
        title: 'Requests',
        series: [
            { name: '2xx', data: [1], color: 'green-lighten1' },
            { name: 'unknown', data: [2], color: 'black' },
        ],
    });
    assert.equal(chart.series[0].color, undefined, 'the chart itself is not changed');
    assert.equal(withSeriesColors(undefined, TRACE_STATUS_COLORS), undefined);
});
