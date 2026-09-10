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

test('buildTraceQuickFilters uses backend counts for root service and span names', async () => {
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
            key: 'ServiceName',
            values: [
                { value: 'flask-demo', count: 40 },
                { value: 'express-demo', count: 12 },
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
        ['Namespace', 'ServiceName', 'SpanName'],
    );
    assert.equal(groups[0].label, 'Namespace');
    assert.equal(groups[1].label, 'Application');
    assert.equal(groups[2].label, 'Root span name');
    assert.equal(groups[0].values.find((v) => v.value === 'coroot-dev').count, 40);
    assert.equal(groups[0].values.find((v) => v.value === 'n/a').label, 'Not applicable');
    assert.equal(groups[1].values.find((v) => v.value === 'flask-demo').count, 40);
    assert.equal(groups[2].values.find((v) => v.value === 'GET /api/hello').count, 30);
});

test('buildTraceQuickFilters hides empty groups and ignores unknown keys', async () => {
    const { buildTraceQuickFilters } = await loadTraceQuickFilters();
    const groups = buildTraceQuickFilters([
        { key: 'ServiceName', values: [{ value: 'flask-demo', count: 3 }] },
        { key: 'SpanName', values: [] },
        { key: 'TraceId', values: [{ value: 'abc', count: 1 }] },
    ]);

    assert.deepEqual(
        groups.map((g) => g.key),
        ['ServiceName'],
    );
});
