const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

async function loadSort() {
    const sourcePath = path.resolve(__dirname, '../../src/utils/observabilitySort.js');
    const source = await readFile(sourcePath, 'utf8');
    return import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}#${pathToFileURL(sourcePath)}`);
}

test('toggleSort starts at Date desc then flips, Duration starts at desc', async () => {
    const { toggleSort } = await loadSort();

    assert.deepEqual(toggleSort(undefined, 'date'), { by: 'date', dir: 'asc' });
    assert.deepEqual(toggleSort({ by: 'date', dir: 'desc' }, 'date'), { by: 'date', dir: 'asc' });
    assert.deepEqual(toggleSort({ by: 'date', dir: 'asc' }, 'date'), { by: 'date', dir: 'desc' });
    assert.deepEqual(toggleSort({ by: 'date', dir: 'desc' }, 'duration'), { by: 'duration', dir: 'desc' });
});

test('sort chips round-trip Date/Duration with direction', async () => {
    const { formatSortValue, parseSortValue, appendSortChip, extractSort } = await loadSort();

    assert.equal(formatSortValue({ by: 'date', dir: 'desc' }), 'Date desc');
    assert.equal(formatSortValue({ by: 'duration', dir: 'asc' }), 'Duration asc');
    assert.deepEqual(parseSortValue('Date asc'), { by: 'date', dir: 'asc' });
    assert.deepEqual(parseSortValue('Duration desc'), { by: 'duration', dir: 'desc' });
    assert.equal(parseSortValue('Message desc'), null);

    const withChip = appendSortChip([{ name: 'Application', op: '=', value: 'express-demo' }], { by: 'duration', dir: 'desc' });
    assert.deepEqual(withChip[withChip.length - 1], { name: 'Sort', op: '=', value: 'Duration desc' });

    const { filters, sort } = extractSort([
        { name: 'Sort', op: '=', value: 'Date asc' },
        { name: 'Application', op: '=', value: 'express-demo' },
        { name: 'Sort', op: '=', value: 'Duration desc' },
    ]);
    assert.deepEqual(sort, { by: 'duration', dir: 'desc' });
    assert.deepEqual(filters, [{ name: 'Application', op: '=', value: 'express-demo' }]);
});

test('effective sort is Date desc when query.sort is omitted', async () => {
    const { effectiveSort } = await loadSort();
    assert.deepEqual(effectiveSort(undefined), { by: 'date', dir: 'desc' });
});

test('applying the same sort chip does not rewrite the query', async () => {
    const { appendSortChip, applyBuilderFilters } = await loadSort();
    const current = { view: 'messages', filters: [], limit: 100, sort: { by: 'date', dir: 'asc' } };
    const next = applyBuilderFilters(current, appendSortChip(current.filters, current.sort));
    assert.equal(next, current);
});

test('applying a new sort chip updates sort without duplicating filters', async () => {
    const { applyBuilderFilters } = await loadSort();
    const current = { view: 'messages', filters: [{ name: 'Application', op: '=', value: 'express-demo' }], limit: 100 };
    const next = applyBuilderFilters(current, [
        { name: 'Application', op: '=', value: 'express-demo' },
        { name: 'Sort', op: '=', value: 'Date asc' },
    ]);
    assert.notEqual(next, current);
    assert.deepEqual(next.sort, { by: 'date', dir: 'asc' });
    assert.deepEqual(next.filters, [{ name: 'Application', op: '=', value: 'express-demo' }]);
});

test('sort click plus QueryBuilder chip echo does not keep fetching', async () => {
    const { appendSortChip, applyBuilderFilters, shouldReloadQuery, toggleSort } = await loadSort();
    let query = { view: 'messages', filters: [], limit: 100 };
    let serialized = JSON.stringify(query);
    let fetches = 0;

    query = { ...query, sort: toggleSort(query.sort, 'date') };
    let decision = shouldReloadQuery(serialized, query);
    assert.equal(decision.reload, true);
    fetches += 1;
    serialized = decision.serialized;

    for (let step = 0; step < 50; step++) {
        const chips = appendSortChip(query.filters, query.sort).map((filter) => ({ ...filter }));
        const next = applyBuilderFilters(query, chips);
        assert.equal(next, query, `v-model echo rewrote query on step ${step}`);
        decision = shouldReloadQuery(serialized, next);
        assert.equal(decision.reload, false, `identical query reloaded on step ${step}`);
    }
    assert.equal(fetches, 1);
});

test('route replay of the same query JSON does not reload', async () => {
    const { makeQueryFromRoute, shouldReloadQuery } = await loadSort();
    const query = { view: 'messages', filters: [], limit: 100, sort: { by: 'date', dir: 'asc' } };
    const serialized = JSON.stringify(query);
    const replayed = makeQueryFromRoute(JSON.stringify(query));
    assert.deepEqual(replayed, query);
    assert.equal(shouldReloadQuery(serialized, replayed).reload, false);
});
