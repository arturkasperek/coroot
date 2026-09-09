<template>
    <QuickFilters
        :groups="groups"
        :filters="filters"
        :local-search-keys="localSearchKeys"
        aria-label="Log filters"
        @toggle="$emit('toggle', $event)"
        @clear="$emit('clear')"
    />
</template>

<script>
import QuickFilters from '@/components/QuickFilters.vue';
import { buildLogQuickFilters, buildStableLogQuickFilters } from '@/utils/logQuickFilters';

export default {
    components: { QuickFilters },
    props: {
        entries: { type: Array, default: () => [] },
        filters: { type: Array, default: () => [] },
        hiddenAttributes: { type: Array, default: () => [] },
        columns: { type: Array, default: () => [] },
        severityFacets: { type: Array, default: () => [] },
        facets: { type: Array, default: undefined },
    },
    data() {
        return {
            facetCatalog: {},
        };
    },
    computed: {
        localSearchKeys() {
            return ['Namespace', 'Application', 'host.name'];
        },
        rawGroups() {
            return buildLogQuickFilters(this.entries, {
                hiddenAttributes: this.hiddenAttributes,
                columns: this.columns,
                severityFacets: this.severityFacets,
                facets: this.facets,
            });
        },
        groups() {
            return buildStableLogQuickFilters(this.rawGroups, this.filters, this.facetCatalog, {
                hiddenAttributes: this.hiddenAttributes,
                columns: this.columns,
            });
        },
    },
    watch: {
        rawGroups: {
            immediate: true,
            handler(groups) {
                for (const group of groups) {
                    const existing = this.facetCatalog[group.key];
                    const knownValues = existing ? [...existing.values] : [];
                    let changed = !existing;
                    for (const value of group.values) {
                        if (!knownValues.some((known) => known.value === value.value)) {
                            knownValues.push({
                                value: value.value,
                                label: value.label,
                                color: value.color,
                            });
                            changed = true;
                        }
                    }
                    if (changed) {
                        this.$set(this.facetCatalog, group.key, {
                            key: group.key,
                            label: group.label,
                            values: knownValues,
                        });
                    }
                }
            },
        },
    },
};
</script>
