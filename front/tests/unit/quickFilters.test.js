const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadTraceQuickFilters() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/traceQuickFilters.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('logs and traces share the QuickFilters panel', async () => {
    const [quick, logs, traces] = await Promise.all([
        readFile(path.resolve(__dirname, '../../src/components/QuickFilters.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/components/LogQuickFilters.vue'), 'utf8'),
        readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8'),
    ]);

    assert.match(quick, /Search filters/);
    assert.match(quick, /mdi-plus/);
    assert.match(quick, /mdi-minus/);
    assert.match(logs, /import QuickFilters from ['"]@\/components\/QuickFilters\.vue['"]/);
    assert.match(logs, /<QuickFilters\b/);
    assert.match(traces, /import QuickFilters from ['"]@\/components\/QuickFilters\.vue['"]/);
    assert.match(traces, /<QuickFilters\b/);
});

test('buildTraceQuickFilters uses backend counts for servers and span names', async () => {
    const { buildTraceQuickFilters } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters([
        {
            key: 'Namespace',
            values: [
                { value: 'coroot-dev', count: 40 },
                { value: 'n/a', count: 5 },
            ],
        },
        {
            key: 'Server',
            values: [
                { value: 'coroot-dev/flask-demo', count: 40 },
                { value: 'coroot-dev/express-demo', count: 12 },
            ],
        },
        {
            key: 'ApiRoute',
            values: [
                { value: 'GET /health', count: 22 },
                { value: 'GET /metrics', count: 9 },
            ],
        },
        {
            key: 'SpanName',
            values: [
                { value: 'GET /api/hello', count: 30 },
                { value: 'GET /api/error', count: 8 },
            ],
        },
    ]);

    assert.deepEqual(
        groups.map((g) => g.key),
        ['Namespace', 'Server', 'ApiRoute', 'SpanName'],
    );
    assert.equal(groups[0].label, 'Namespace');
    assert.equal(groups[1].label, 'Server');
    assert.equal(groups[2].label, 'API Route');
    assert.equal(groups[3].label, 'Root span name');
    assert.equal(groups[0].values.find((v) => v.value === 'coroot-dev').count, 40);
    assert.equal(groups[0].values.find((v) => v.value === 'n/a').label, 'Not applicable');
    assert.equal(groups[1].values.find((v) => v.value === 'coroot-dev/flask-demo').count, 40);
    assert.equal(groups[2].values.find((v) => v.value === 'GET /health').count, 22);
    assert.equal(groups[3].values.find((v) => v.value === 'GET /api/hello').count, 30);
});

test('buildTraceQuickFilters hides empty groups and ignores unknown keys', async () => {
    const { buildTraceQuickFilters } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters([
        { key: 'Server', values: [{ value: 'flask-demo', count: 3 }] },
        { key: 'SpanName', values: [] },
        { key: 'TraceId', values: [{ value: 'abc', count: 1 }] },
    ]);

    assert.deepEqual(
        groups.map((g) => g.key),
        ['Server'],
    );
});

test('buildTraceQuickFilters puts the Source facet first and names its values', async () => {
    const { buildTraceQuickFilters, displayTraceSourceName } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters([
        { key: 'Server', values: [{ value: 'checkout', count: 3 }] },
        {
            key: 'Source',
            values: [
                { value: 'agent', count: 40 },
                { value: 'otel', count: 3 },
            ],
        },
    ]);

    assert.deepEqual(
        groups.map((g) => g.key),
        ['Source', 'Server'],
    );
    assert.deepEqual(
        groups[0].values.map((v) => [v.value, v.label, v.count]),
        [
            ['agent', 'eBPF (node-agent)', 40],
            ['otel', 'OpenTelemetry', 3],
        ],
    );
    assert.equal(groups[0].label, 'Source');
    assert.equal(displayTraceSourceName('other'), 'other');
});

test('buildTraceQuickFilters shows the status classes after Source, with the colors of the chart', async () => {
    const { buildTraceQuickFilters } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters(
        [
            { key: 'Server', values: [{ value: 'flask-demo', count: 40 }] },
            {
                key: 'Status',
                values: [
                    { value: '2xx', count: 30 },
                    { value: '5xx', count: 8 },
                ],
            },
            { key: 'Source', values: [{ value: 'otel', count: 40 }] },
        ],
        { statusColors: { '2xx': '#66bb6a', '5xx': '#ef5350' } },
    );
    assert.deepEqual(
        groups.map((group) => group.key),
        ['Source', 'Status', 'Server'],
    );
    assert.deepEqual(groups[1], {
        key: 'Status',
        label: 'Status',
        values: [
            { value: '2xx', label: '2xx', count: 30, color: '#66bb6a' },
            { value: '5xx', label: '5xx', count: 8, color: '#ef5350' },
        ],
    });
    assert.equal(groups[2].values[0].color, '', 'other facets have no color');
});

test('buildTraceQuickFilters shows Server and Client after Status, as the collector names them', async () => {
    const { buildTraceQuickFilters } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters([
        { key: 'Client', values: [{ value: 'coroot-dev/demo-traffic', count: 30 }] },
        {
            key: 'Server',
            values: [
                { value: 'coroot-dev/express-demo', count: 20 },
                { value: '10.99.0.1:5432', count: 2 },
            ],
        },
        { key: 'ServiceName', values: [{ value: 'express-demo', count: 20 }] }, // not a facet of the Traces page
    ]);
    assert.deepEqual(
        groups.map((group) => [group.key, group.label]),
        [
            ['Server', 'Server'],
            ['Client', 'Client'],
        ],
        'no Application (ServiceName) group',
    );
    assert.deepEqual(
        groups[0].values.map((value) => [value.value, value.label]),
        [
            ['coroot-dev/express-demo', 'coroot-dev/express-demo'],
            ['10.99.0.1:5432', '10.99.0.1:5432'],
        ],
    );
});
