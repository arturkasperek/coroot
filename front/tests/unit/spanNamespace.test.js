const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadSpanNamespace() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/spanNamespace.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('reads k8s namespace from resource attributes', async () => {
    const { spanK8sNamespace } = await loadSpanNamespace();

    assert.equal(spanK8sNamespace({ 'k8s.namespace.name': 'coroot-dev', Cluster: 'default' }), 'coroot-dev');
    assert.equal(spanK8sNamespace({ Cluster: 'default' }), '');
    assert.equal(spanK8sNamespace(undefined), '');
});

test('span details show namespace after service', async () => {
    const source = await readFile(path.resolve(__dirname, '../../src/components/TracingSpan.vue'), 'utf8');
    const serviceIdx = source.indexOf('<td>service</td>');
    const nsIdx = source.indexOf('<td>namespace</td>');

    assert.ok(serviceIdx >= 0, 'service row missing');
    assert.ok(nsIdx > serviceIdx, 'namespace row must follow service');
    assert.match(source, /spanK8sNamespace/);
    assert.match(source, /v-if="namespace"/);
});
