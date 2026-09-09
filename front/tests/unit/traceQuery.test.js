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

test('shows trace fields with user-facing names in the query builder', async () => {
    const { TRACE_QUERY_FIELDS } = await loadTraceQuery();

    assert.deepEqual(TRACE_QUERY_FIELDS, ['Root Service Name', 'Root Span Name', 'Trace ID']);
});

test('maps existing trace filters to query-builder filters', async () => {
    const { toQueryBuilderFilters } = await loadTraceQuery();

    assert.deepEqual(
        toQueryBuilderFilters([
            { field: 'ServiceName', op: '=', value: 'express-demo' },
            { field: 'SpanName', op: '~', value: 'GET.*' },
            { field: 'TraceId', op: '=', value: 'abc123' },
        ]),
        [
            { name: 'Root Service Name', op: '=', value: 'express-demo' },
            { name: 'Root Span Name', op: '~', value: 'GET.*' },
            { name: 'Trace ID', op: '=', value: 'abc123' },
        ],
    );
});

test('maps query-builder filters back to the trace API schema', async () => {
    const { fromQueryBuilderFilters } = await loadTraceQuery();

    assert.deepEqual(
        fromQueryBuilderFilters([
            { name: 'Root Service Name', op: '!=', value: 'worker' },
            { name: 'Root Span Name', op: '=', value: 'GET /api/hello' },
            { name: 'Trace ID', op: '=', value: 'abc123' },
        ]),
        [
            { field: 'ServiceName', op: '!=', value: 'worker' },
            { field: 'SpanName', op: '=', value: 'GET /api/hello' },
            { field: 'TraceId', op: '=', value: 'abc123' },
        ],
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

test('traces puts Query above the heatmap and hides unsupported controls', async () => {
    const traces = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');

    assert.ok(traces.indexOf('<QueryPanel') < traces.indexOf('<Heatmap'));
    assert.doesNotMatch(traces, /Integrate OpenTelemetry/);
    assert.doesNotMatch(traces, /Exclude auxiliary requests/);
    assert.doesNotMatch(traces, /excludeAux/);
    assert.match(traces, /include_aux: true/);
    assert.doesNotMatch(traces, /select a chart area to see traces/);
});

test('traces sidebar only exposes root service and span name groups', async () => {
    const traces = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');

    assert.match(traces, /buildTraceQuickFilters/);
    assert.match(traces, /view\.facets/);
    assert.doesNotMatch(traces, /Root ID/);
});
