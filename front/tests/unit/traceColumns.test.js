const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadTraceColumns() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/traceColumns.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('API route includes method and prefers http.route, then path without query', async () => {
    const { traceApiRoute } = await loadTraceColumns();

    assert.equal(
        traceApiRoute({ 'http.method': 'GET', 'http.route': '/api/hello', 'http.target': '/api/hello?token=1' }),
        'GET /api/hello',
    );
    assert.equal(traceApiRoute({ 'http.method': 'GET', 'http.target': '/metrics' }), 'GET /metrics');
    assert.equal(traceApiRoute({ 'http.request.method': 'POST', 'http.target': '/api/chain?token=abc' }), 'POST /api/chain');
    assert.equal(traceApiRoute({ 'http.method': 'GET', 'url.path': '/health' }), 'GET /health');
    assert.equal(traceApiRoute({ 'http.method': 'GET', 'http.url': 'http://express-demo:3000/api/slow' }), 'GET /api/slow');
    assert.equal(traceApiRoute({ 'http.target': '/metrics' }), '/metrics');
    assert.equal(traceApiRoute({ Cluster: 'default' }), '');
    assert.equal(traceApiRoute(undefined), '');
});

test('Name collapses to a ditto mark when it repeats the API route', async () => {
    const { TRACE_NAME_SAME_MARK, traceDisplayName } = await loadTraceColumns();

    assert.equal(TRACE_NAME_SAME_MARK, '=');
    assert.equal(traceDisplayName('GET /metrics', 'GET /metrics'), '=');
    assert.equal(traceDisplayName('GET /metrics', '/metrics'), '=');
    assert.equal(traceDisplayName('/metrics', 'GET /metrics'), '=');
    assert.equal(traceDisplayName('GET', 'GET /metrics'), 'GET');
    assert.equal(traceDisplayName('GET /chain', 'GET /chain'), '=');
    assert.equal(traceDisplayName('getServerSideProps', 'GET /chain'), 'getServerSideProps');
    assert.equal(traceDisplayName('GET /api/hello', ''), 'GET /api/hello');
});

test('traces list shows API Route before Name', async () => {
    const source = await readFile(path.resolve(__dirname, '../../src/views/Traces.vue'), 'utf8');
    const routeIdx = source.indexOf("text: 'API Route'");
    const nameIdx = source.indexOf("text: 'Name'");
    assert.ok(routeIdx >= 0, 'API Route column missing');
    assert.ok(nameIdx > routeIdx, 'API Route must come before Name');
    assert.match(source, /traceApiRoute|traceDisplayName/);
});
