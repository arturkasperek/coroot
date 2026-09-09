<template>
    <aside v-if="groups.length" class="panel" :class="{ collapsed }" aria-label="Log filters">
        <button v-if="collapsed" type="button" class="show-filters" title="Show filters" @click="collapsed = false">
            <v-icon small>mdi-filter-variant</v-icon>
            <span>Filters</span>
            <span v-if="activeFilterCount" class="active-count">{{ activeFilterCount }}</span>
        </button>

        <template v-else>
            <div class="panel-header">
                <div class="panel-title">
                    <v-icon small>mdi-filter-variant</v-icon>
                    <span>Filters</span>
                    <span v-if="activeFilterCount" class="active-count">{{ activeFilterCount }}</span>
                </div>
                <div class="panel-actions">
                    <button v-if="activeFilterCount" type="button" class="text-action" @click="$emit('clear')">Clear</button>
                    <button type="button" class="icon-action" title="Hide filters" aria-label="Hide filters" @click="collapsed = true">
                        <v-icon small>mdi-chevron-double-left</v-icon>
                    </button>
                </div>
            </div>

            <v-text-field
                v-model="search"
                placeholder="Search filters"
                aria-label="Search filters"
                dense
                hide-details
                outlined
                clearable
                prepend-inner-icon="mdi-magnify"
                class="search"
            />

            <div class="groups">
                <section v-for="group in visibleGroups" :key="group.key" class="group">
                    <button type="button" class="group-header" :aria-expanded="String(!isCollapsed(group.key))" @click="toggleGroup(group.key)">
                        <v-icon small>{{ isCollapsed(group.key) ? 'mdi-chevron-right' : 'mdi-chevron-down' }}</v-icon>
                        <span class="group-label">{{ group.label }}</span>
                        <span class="group-total">{{ group.values.length }}</span>
                    </button>

                    <div v-if="!isCollapsed(group.key)" class="group-values">
                        <v-text-field
                            v-if="groupHasLocalSearch(group.key)"
                            :value="groupSearch[group.key] || ''"
                            :placeholder="`Search ${group.label.toLowerCase()}`"
                            :aria-label="`Search ${group.label}`"
                            dense
                            hide-details
                            outlined
                            clearable
                            prepend-inner-icon="mdi-magnify"
                            class="search group-search"
                            @input="setGroupSearch(group.key, $event)"
                            @click.stop
                        />
                        <div v-for="opt in shownValues(group)" :key="opt.value" class="facet-row" :title="opt.value">
                            <span class="filter-actions">
                                <button
                                    type="button"
                                    class="filter-action include"
                                    :class="{ active: isActive(group.key, '=', opt.value) }"
                                    :aria-label="`Include only ${opt.label}`"
                                    :title="`Show only ${opt.label}`"
                                    :aria-pressed="String(isActive(group.key, '=', opt.value))"
                                    @click="applyFilter(group, opt, '=')"
                                >
                                    <v-icon x-small>mdi-plus</v-icon>
                                </button>
                                <button
                                    type="button"
                                    class="filter-action exclude"
                                    :class="{ active: isActive(group.key, '!=', opt.value) }"
                                    :aria-label="`Exclude ${opt.label}`"
                                    :title="`Exclude ${opt.label}`"
                                    :aria-pressed="String(isActive(group.key, '!=', opt.value))"
                                    @click="applyFilter(group, opt, '!=')"
                                >
                                    <v-icon x-small>mdi-minus</v-icon>
                                </button>
                            </span>
                            <span v-if="opt.color" class="value-marker" :style="{ backgroundColor: opt.color }" aria-hidden="true" />
                            <span class="name">{{ opt.label }}</span>
                            <span class="count">{{ formatCount(opt.count) }}</span>
                        </div>
                        <div v-if="groupSearch[group.key] && !filteredValues(group).length" class="group-empty">
                            No matching {{ group.label.toLowerCase() }}
                        </div>
                        <button v-if="hasMore(group)" type="button" class="more" @click="loadMore(group)">
                            Show more
                            <v-icon x-small>mdi-chevron-down</v-icon>
                        </button>
                    </div>
                </section>
            </div>

            <div v-if="search && !visibleGroups.length" class="empty-state">No matching filters</div>
        </template>
    </aside>
</template>

<script>
import {
    buildLogQuickFilters,
    buildStableLogQuickFilters,
    filterFacetValues,
    formatCount,
    groupHasLocalSearch,
    isLogFacetActive,
} from '@/utils/logQuickFilters';

const PAGE = 10;

export default {
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
            search: '',
            groupSearch: {},
            extra: {},
            collapsedGroups: {},
            collapsed: false,
            facetCatalog: {},
        };
    },
    computed: {
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
        activeFilterCount() {
            return (this.filters || []).length;
        },
        visibleGroups() {
            const q = (this.search || '').trim().toLowerCase();
            if (!q) {
                return this.groups;
            }
            return this.groups
                .map((g) => ({
                    ...g,
                    values: g.values.filter(
                        (v) => g.label.toLowerCase().includes(q) || v.label.toLowerCase().includes(q) || v.value.toLowerCase().includes(q),
                    ),
                }))
                .filter((g) => g.values.length > 0);
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
    methods: {
        formatCount,
        groupHasLocalSearch,
        setGroupSearch(key, value) {
            this.$set(this.groupSearch, key, value || '');
            this.$set(this.extra, key, 0);
        },
        filteredValues(group) {
            return filterFacetValues(group.values, this.groupSearch[group.key]);
        },
        isActive(name, op, value) {
            return isLogFacetActive(this.filters, name, op, value);
        },
        isCollapsed(key) {
            return Boolean(this.collapsedGroups[key]) && !this.search;
        },
        toggleGroup(key) {
            this.$set(this.collapsedGroups, key, !this.collapsedGroups[key]);
        },
        shownValues(group) {
            return this.filteredValues(group).slice(0, PAGE + (this.extra[group.key] || 0));
        },
        hasMore(group) {
            return this.shownValues(group).length < this.filteredValues(group).length;
        },
        loadMore(group) {
            this.$set(this.extra, group.key, (this.extra[group.key] || 0) + PAGE);
        },
        applyFilter(group, opt, op) {
            this.$emit('toggle', { name: group.key, op, value: opt.value });
        },
    },
};
</script>

<style scoped>
.panel {
    width: 280px;
    height: 100%;
    flex: 0 0 280px;
    overflow-x: hidden;
    overflow-y: auto;
    overscroll-behavior: contain;
    scrollbar-width: thin;
    display: flex;
    flex-direction: column;
    padding-right: 16px;
    border-right: 1px solid var(--border-color);
    color: var(--text-color);
    transition:
        width 0.15s ease,
        flex-basis 0.15s ease;
}
.panel.collapsed {
    width: 42px;
    flex-basis: 42px;
    padding-right: 8px;
    overflow: hidden;
}
.panel-header {
    min-height: 36px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
}
.panel-title,
.panel-actions {
    display: flex;
    align-items: center;
}
.panel-title {
    gap: 7px;
    font-size: 14px;
    font-weight: 600;
}
.panel-actions {
    gap: 2px;
}
.active-count {
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 9px;
    background: var(--background-color-hi);
    color: var(--text-color-dimmed);
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
}
.text-action,
.icon-action,
.show-filters {
    border: 0;
    background: transparent;
    color: var(--text-color-dimmed);
    cursor: pointer;
}
.text-action {
    padding: 4px 6px;
    border-radius: 4px;
    font-size: 12px;
}
.icon-action {
    width: 28px;
    height: 28px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
}
.text-action:hover,
.icon-action:hover,
.show-filters:hover {
    color: var(--text-color);
    background: var(--background-color-hi);
}
.show-filters {
    width: 33px;
    min-height: 96px;
    padding: 8px 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 7px;
    border-radius: 4px;
}
.show-filters > span:not(.active-count) {
    writing-mode: vertical-rl;
    transform: rotate(180deg);
    font-size: 12px;
}
.search {
    flex: 0 0 auto;
    margin-bottom: 8px;
}
.search:deep(.v-input__slot) {
    min-height: 38px !important;
    padding: 0 10px !important;
    border-radius: 5px !important;
    background: var(--background-color);
}
.search:deep(.v-input__prepend-inner) {
    margin-top: 7px !important;
}
.search:deep(.v-input__append-inner) {
    margin-top: 7px !important;
}
.search:deep(input) {
    font-size: 13px;
}
.group-search {
    margin: 0 7px 6px;
}
.group-empty {
    padding: 6px 7px 10px 58px;
    color: var(--text-color-dimmed);
    font-size: 12px;
}
.groups {
    padding-right: 4px;
}
.group {
    border-bottom: 1px solid var(--border-color);
}
.group:first-child {
    border-top: 1px solid var(--border-color);
}
.group-header {
    width: 100%;
    min-height: 40px;
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 0 5px 0 2px;
    border: 0;
    background: transparent;
    color: var(--text-color-dimmed);
    cursor: pointer;
    text-align: left;
}
.group-header:hover {
    color: var(--text-color);
    background: var(--background-color-hi);
}
.group-label {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.025em;
    text-transform: uppercase;
}
.group-total {
    flex: 0 0 auto;
    font-size: 11px;
    font-variant-numeric: tabular-nums;
    opacity: 0.75;
}
.group-values {
    padding: 0 0 8px;
}
.facet-row {
    width: 100%;
    min-height: 36px;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 7px;
    border-radius: 4px;
    color: var(--text-color-dimmed);
    font-size: 13px;
}
.facet-row:hover {
    color: var(--text-color);
    background: var(--background-color-hi);
}
.filter-actions {
    flex: 0 0 auto;
    display: inline-flex;
    gap: 3px;
}
.filter-action {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: 1px solid var(--border-color);
    border-radius: 3px;
    background: transparent;
    color: var(--text-color-dimmed);
    cursor: pointer;
}
.filter-action:hover,
.filter-action:focus-visible {
    color: var(--text-color);
    border-color: var(--text-color-dimmed);
    background: var(--background-color-hi);
    outline: none;
}
.filter-action.include.active {
    color: #fff;
    border-color: var(--primary-color, #1976d2);
    background: var(--primary-color, #1976d2);
}
.filter-action.exclude.active {
    color: #fff;
    border-color: #d32f2f;
    background: #d32f2f;
}
.value-marker {
    flex: 0 0 3px;
    width: 3px;
    height: 16px;
    border-radius: 2px;
    filter: brightness(var(--brightness));
}
.name {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.count {
    flex: 0 0 auto;
    color: var(--text-color-dimmed);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
}
.more {
    min-height: 30px;
    margin-left: 58px;
    padding: 3px 7px;
    display: inline-flex;
    align-items: center;
    gap: 2px;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--primary-color, #1976d2);
    font-size: 12px;
    cursor: pointer;
}
.more:hover {
    background: var(--background-color-hi);
}
.empty-state {
    padding: 24px 8px;
    color: var(--text-color-dimmed);
    font-size: 12px;
    text-align: center;
}
@media (max-width: 960px) {
    .panel {
        width: 240px;
        flex-basis: 240px;
    }
}
</style>
