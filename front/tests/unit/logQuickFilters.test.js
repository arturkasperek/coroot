const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadLogQuickFilters() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/logQuickFilters.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('restores active include and exclude controls from deserialized filters', async () => {
    const { isLogFacetActive } = await loadLogQuickFilters();
    const filters = [
        { name: 'Severity', op: '=', value: 'info' },
        { name: 'Severity', op: '!=', value: 'warning' },
    ];

    assert.equal(isLogFacetActive(filters, 'Severity', '=', 'info'), true);
    assert.equal(isLogFacetActive(filters, 'Severity', '!=', 'warning'), true);
    assert.equal(isLogFacetActive(filters, 'Severity', '=', 'warning'), false);
});

test('keeps an active excluded value visible when it is absent from filtered entries', async () => {
    const { buildStableLogQuickFilters, isLogFacetActive } = await loadLogQuickFilters();
    const rawGroups = [
        {
            key: 'service.name',
            label: 'Application',
            values: [{ value: '/k8s/coroot-dev/coroot', label: 'coroot-dev/coroot', count: 4, color: '' }],
        },
    ];
    const filters = [{ name: 'service.name', op: '!=', value: 'express-demo' }];

    const groups = buildStableLogQuickFilters(rawGroups, filters);
    const applications = groups.find((group) => group.key === 'service.name');
    const excluded = applications.values.find((value) => value.value === 'express-demo');

    assert.deepEqual(excluded, {
        value: 'express-demo',
        label: 'express-demo',
        color: '',
        count: 0,
    });
    assert.equal(isLogFacetActive(filters, 'service.name', '!=', excluded.value), true);
});
