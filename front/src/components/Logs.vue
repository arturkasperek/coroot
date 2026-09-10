<template>
    <div class="logs">
        <v-alert v-if="view.message" color="info" outlined text class="message">
            {{ view.message }}
        </v-alert>
        <template v-else>
            <v-alert v-if="view.error" color="error" icon="mdi-alert-octagon-outline" outlined text class="mt-2">
                {{ view.error }}
            </v-alert>

            <QueryPanel
                v-model="query.filters"
                :options-loading="qb.loading"
                :items="qb.items"
                :error="qb.error"
                :disabled="query.view !== 'messages'"
                :hidden-attributes="hiddenAttributes"
                @get="qbGet"
            >
                <template #actions>
                    <LogSearchButtons :interval="refreshInterval" @search="get" @refresh="setRefreshInterval" />
                </template>
            </QueryPanel>

            <Chart
                v-if="view.chart"
                :key="query.view"
                :chart="view.chart"
                :selection="{}"
                @select="zoom"
                @legend-click="showLegendMenu"
                class="my-3"
            />

            <v-menu v-model="legendMenu.show" :position-x="legendMenu.x" :position-y="legendMenu.y" absolute offset-y>
                <v-list dense>
                    <v-list-item @click="addLegendFilter('=')">
                        <v-icon small class="mr-1">mdi-plus</v-icon>
                        Show only {{ legendMenu.label }}
                    </v-list-item>
                    <v-list-item @click="addLegendFilter('!=')">
                        <v-icon small class="mr-1">mdi-minus</v-icon>
                        Exclude {{ legendMenu.label }}
                    </v-list-item>
                </v-list>
            </v-menu>

            <div v-if="query.view === 'messages'" ref="logsBody" class="logs-body" :style="{ height: `${logsBodyHeight}px` }">
                <LogQuickFilters
                    :entries="entries"
                    :filters="query.filters"
                    :hidden-attributes="hiddenAttributes"
                    :columns="columns"
                    :severity-facets="severityFacets"
                    :facets="view.facets"
                    @toggle="toggleQuickFilter"
                    @clear="clearQuickFilters"
                />
                <div class="logs-table">
                    <ObservabilityTable
                        :headers="columns"
                        :items="entries"
                        :loading="loading"
                        :row-key="(item, index) => `${item.timestamp}-${index}`"
                        :row-color="(item) => item.color"
                        :value-getter="getColumnValue"
                        empty-text="No messages found"
                        mono
                        clickable
                        @row-click="entry = $event"
                    >
                        <template #item.application="{ item, value }">
                            <v-menu offset-y @click.stop>
                                <template #activator="{ on }">
                                    <a v-on="on" class="nowrap" style="display: inline-block; max-width: 20ch" @click.stop>{{ value }}</a>
                                </template>
                                <v-list dense>
                                    <template v-if="item.attributes['service.name']">
                                        <v-list-item @click="qbAdd('service.name', '=', item.attributes['service.name'])">
                                            <v-icon small class="mr-1">mdi-plus</v-icon>
                                            add to search
                                        </v-list-item>
                                        <v-list-item @click="qbAdd('service.name', '!=', item.attributes['service.name'])">
                                            <v-icon small class="mr-1">mdi-minus</v-icon>
                                            exclude from search
                                        </v-list-item>
                                    </template>
                                    <v-list-item v-if="item.link" :to="item.link">
                                        <v-icon small class="mr-1">mdi-open-in-new</v-icon>
                                        go to application
                                    </v-list-item>
                                </v-list>
                            </v-menu>
                        </template>
                        <template v-for="col in columns.filter((col) => col.key !== 'application')" #[`item.${col.key}`]="{ value }">
                            <span v-if="col.maxWidth" :key="col.key" :title="value">
                                {{ truncateText(value, col.maxWidth) }}
                            </span>
                            <span v-else :key="col.key">{{ value }}</span>
                        </template>
                    </ObservabilityTable>
                    <div v-if="entries.length === query.limit" class="text-right caption grey--text mt-1">
                        The output is capped at
                        <InlineSelect v-model="query.limit" :items="limits" />
                        messages.
                    </div>
                    <LogEntry v-if="entry" v-model="entry" @filter="qbAdd" />
                </div>
            </div>
        </template>
    </div>
</template>

<script>
import { palette } from '@/utils/colors';
import QueryPanel from '@/components/QueryPanel.vue';
import Chart from '@/components/Chart.vue';
import LogEntry from '@/components/LogEntry.vue';
import InlineSelect from '@/components/InlineSelect.vue';
import LogSearchButtons from '@/components/LogSearchButtons.vue';
import LogQuickFilters from '@/components/LogQuickFilters.vue';
import ObservabilityTable from '@/components/ObservabilityTable.vue';

const SEVERITY_FACETS = [
    { value: 'unknown', color: 'grey-lighten1' },
    { value: 'info', color: 'blue-lighten2' },
    { value: 'warning', color: 'orange-lighten1' },
    { value: 'error', color: 'red-darken1' },
];

export default {
    components: { ObservabilityTable, LogQuickFilters, LogSearchButtons, InlineSelect, LogEntry, Chart, QueryPanel },

    props: {
        defaultFilters: {
            type: Array,
            default: () => [],
        },
        hiddenAttributes: {
            type: Array,
            default: () => [],
        },
        columns: {
            type: Array,
            default: () => [
                { key: 'date', label: 'Date' },
                { key: 'cluster', label: 'Cluster', maxWidth: 20 },
                { key: 'application', label: 'Application' },
                { key: 'message', label: 'Message' },
            ],
        },
    },
    data() {
        let q = {};
        try {
            q = JSON.parse(this.$route.query.query || '{}');
        } catch {
            //
        }
        return {
            loading: false,
            error: '',
            view: {},
            refreshInterval: 0,
            logsBodyHeight: 0,
            query: this.makeQuery(q),
            limits: [10, 20, 50, 100, 1000],
            entry: null,
            qb: {
                loading: false,
                error: '',
                items: [],
            },
            legendMenu: {
                show: false,
                x: 0,
                y: 0,
                label: '',
                field: '',
                value: '',
            },
        };
    },

    mounted() {
        this.get();
        this.$events.watch(this, this.get, 'refresh');
        window.addEventListener('resize', this.scheduleLogsBodyResize);
        window.visualViewport?.addEventListener('resize', this.scheduleLogsBodyResize);
        this.scheduleLogsBodyResize();
    },

    updated() {
        this.scheduleLogsBodyResize();
    },

    beforeDestroy() {
        this.refreshInterval = 0;
        window.removeEventListener('resize', this.scheduleLogsBodyResize);
        window.visualViewport?.removeEventListener('resize', this.scheduleLogsBodyResize);
        cancelAnimationFrame(this._logsBodyResizeFrame);
    },

    watch: {
        $route: {
            handler(newRoute, oldRoute) {
                if (newRoute.query.query !== oldRoute?.query.query) {
                    let q = {};
                    try {
                        q = JSON.parse(this.$route.query.query || '{}');
                    } catch {
                        //
                    }
                    this.query = this.makeQuery(q);
                }
            },
        },
        query: {
            handler(curr, prev) {
                this.setQuery(curr.view !== prev.view);
                this.get();
            },
            deep: true,
        },
        loading(val) {
            this.$emit('loading', val);
        },
        error(val) {
            this.$emit('error', val);
        },
    },

    computed: {
        queryWithDefaults() {
            return {
                ...this.query,
                filters: [...this.defaultFilters, ...this.query.filters],
            };
        },
        severityFacets() {
            const series = (this.view.chart && this.view.chart.series) || [];
            return SEVERITY_FACETS.map((facet) => {
                const match = series.find((item) => item.name === facet.value);
                const count = match ? (match.data || []).reduce((total, value) => total + (Number(value) || 0), 0) : 0;
                return {
                    value: facet.value,
                    count,
                    color: palette.get((match && match.color) || facet.color),
                };
            });
        },
        entries() {
            if (!this.view.entries) {
                return [];
            }
            const sorted = [...this.view.entries].sort((a, b) => b.timestamp - a.timestamp);
            if (sorted.length > this.query.limit) {
                sorted.splice(this.query.limit);
            }
            return sorted.map((e) => {
                const message = e.message.trim();
                const newline = message.indexOf('\n');
                let application = e.application;
                let link;
                if (e.application.includes(':')) {
                    const id = this.$utils.appId(e.application);
                    application = id.name;
                    link = {
                        name: 'overview',
                        params: { view: 'applications', id: e.application, report: 'Logs' },
                        query: this.$utils.contextQuery(),
                    };
                }
                return {
                    ...e,
                    application,
                    link,
                    message,
                    color: palette.get(e.color),
                    date: this.$format.date(e.timestamp, '{MMM} {DD} {HH}:{mm}:{ss}'),
                    multiline: newline > 0 ? newline : 0,
                };
            });
        },
    },

    methods: {
        scheduleLogsBodyResize() {
            cancelAnimationFrame(this._logsBodyResizeFrame);
            this._logsBodyResizeFrame = requestAnimationFrame(() => {
                this._logsBodyResizeFrame = 0;
                this.updateLogsBodyHeight();
            });
        },
        updateLogsBodyHeight() {
            const body = this.$refs.logsBody;
            if (!body) {
                return;
            }
            const container = body.closest('.container');
            const bottomPadding = container ? Number.parseFloat(getComputedStyle(container).paddingBottom) || 0 : 0;
            const viewport = window.visualViewport;
            const viewportBottom = viewport ? viewport.offsetTop + viewport.height : window.innerHeight;
            const nextHeight = Math.max(0, Math.floor(viewportBottom - body.getBoundingClientRect().top - bottomPadding));
            if (this.logsBodyHeight !== nextHeight) {
                this.logsBodyHeight = nextHeight;
            }
        },
        makeQuery(q) {
            return {
                view: q.view || 'messages',
                filters: q.filters || [],
                limit: q.limit || 100,
            };
        },
        setQuery(push) {
            const to = { query: { ...this.$route.query, query: JSON.stringify(this.query) } };
            if (push) {
                this.$router.push(to).catch((err) => err);
            } else {
                this.$router.replace(to).catch((err) => err);
            }
        },
        openView(v) {
            this.query.view = v;
        },
        qbAdd(name, op, value) {
            this.query.view = 'messages';
            this.entry = null;
            this.pattern = null;
            this.query.filters.push({ name, op, value });
        },
        clearQuickFilters() {
            this.query.view = 'messages';
            this.entry = null;
            this.pattern = null;
            this.query.filters = [];
        },
        toggleQuickFilter({ name, op, value }) {
            this.query.view = 'messages';
            this.entry = null;
            this.pattern = null;
            const idx = this.query.filters.findIndex((f) => f.name === name && f.op === op && f.value === value);
            if (idx >= 0) {
                this.query.filters.splice(idx, 1);
                return;
            }
            this.query.filters = this.query.filters.filter((f) => !(f.name === name && f.value === value));
            this.query.filters.push({ name, op, value });
        },
        qbGet(what, name) {
            this.qb.items = [];
            if (what === 'op') {
                switch (name) {
                    case 'Severity':
                    case 'Source':
                    case 'Namespace':
                    case 'Application':
                        this.qb.items = ['=', '!='];
                        break;
                    case 'Message':
                        this.qb.items = ['contains', 'not contains'];
                        break;
                    case 'pattern.hash':
                        this.qb.items = ['='];
                        break;
                    default:
                        this.qb.items = ['=', '!=', '~', '!~'];
                }
                return;
            }
            if (what === 'name') {
                this.qb.items = this.view.suggest || [];
                return;
            }
            this.qb.loading = true;
            this.qb.error = '';
            const query = JSON.stringify({ ...this.queryWithDefaults, suggest: name });
            this.$api.getOverview('logs', query, (data, error) => {
                this.qb.loading = false;
                if (error || data.status === 'warning') {
                    this.qb.error = error || data.message;
                    return;
                }
                this.qb.items = data.logs.suggest || [];
            });
        },
        get() {
            this.refreshInterval = 0;
            this.loading = true;
            this.error = '';
            this.$api.getOverview('logs', JSON.stringify(this.queryWithDefaults), (data, error) => {
                this.loading = false;
                if (error) {
                    this.error = error;
                    return;
                }
                this.view = data.logs || {};
            });
        },
        startRefresh(interval) {
            if (this.$route.query.to) {
                this.$route.query.to = undefined;
                this.setQuery(false);
            }
            let since = this.view.max_ts || '';
            const refresh = () => {
                if (!this.refreshInterval) return;
                if (document.hidden) {
                    setTimeout(refresh, interval);
                    return;
                }
                this.loading = true;
                this.error = '';
                const query = { ...this.queryWithDefaults, since };
                const started = Date.now();
                this.$api.getOverview('logs', JSON.stringify(query), (data, error) => {
                    this.loading = false;
                    const elapsed = Date.now() - started;
                    setTimeout(refresh, Math.max(0, interval - elapsed));
                    if (error) {
                        this.error = error;
                        return;
                    }
                    this.view.error = data.logs.error;
                    this.view.message = data.logs.message;
                    this.view.chart = data.logs.chart;
                    if (data.logs.max_ts) {
                        since = data.logs.max_ts;
                    }
                    if (data.logs.entries) {
                        this.view.entries.push(...data.logs.entries);
                    }
                    if (Array.isArray(data.logs.facets)) {
                        this.view.facets = data.logs.facets;
                    }
                });
            };
            refresh();
        },
        setRefreshInterval(interval) {
            this.refreshInterval = interval;
            this.refreshInterval && this.startRefresh(this.refreshInterval * 1000);
        },
        zoom(s) {
            const { from, to } = s.selection;
            const query = { ...this.$route.query, from, to };
            this.$router.push({ query }).catch((err) => err);
        },
        getColumnValue(entry, column) {
            switch (column.key) {
                case 'date':
                    return entry.date;
                case 'application':
                    return entry.application;
                case 'message':
                    return entry.multiline ? entry.message.substr(0, entry.multiline) : entry.message;
                case 'cluster':
                    return entry.cluster;
                default:
                    // For custom attributes, look in the entry.attributes
                    return entry.attributes[column.key] || '';
            }
        },
        truncateText(text, maxLength) {
            if (!text) return '';
            return text.length > maxLength ? text.substring(0, maxLength) + '...' : text;
        },
        showLegendMenu(event) {
            this.legendMenu = {
                show: true,
                x: event.x,
                y: event.y,
                field: 'Severity',
                value: event.value,
                label: event.label || event.value,
            };
        },
        addLegendFilter(operator) {
            this.legendMenu.show = false;

            const sameFilterFound = this.query.filters.find(
                (f) => f.name === this.legendMenu.field && f.op === operator && f.value === this.legendMenu.value,
            );

            if (sameFilterFound) {
                return;
            }
            const conflictingFilter = this.query.filters.find((f) => f.name === this.legendMenu.field && f.value === this.legendMenu.value);

            if (conflictingFilter) {
                const index = this.query.filters.indexOf(conflictingFilter);
                this.query.filters.splice(index, 1, {
                    name: this.legendMenu.field,
                    op: operator,
                    value: this.legendMenu.value,
                });
            } else {
                this.qbAdd(this.legendMenu.field, operator, this.legendMenu.value);
            }
        },
    },
};
</script>

<style scoped>
.view {
    color: var(--text-color-dimmed);
}
.view.active {
    color: var(--text-color);
    border-bottom: 2px solid var(--text-color);
}
.logs-body {
    display: flex;
    align-items: stretch;
    gap: 16px;
    min-height: 0;
    overflow: hidden;
}
.logs-table {
    flex: 1 1 auto;
    min-width: 0;
    height: 100%;
    overflow: auto;
    overscroll-behavior: contain;
    scrollbar-width: thin;
}
.logs-table:deep(.v-data-table__wrapper) {
    overflow: visible;
}
.logs-table:deep(thead th) {
    position: sticky;
    top: 0;
    z-index: 2;
    background: var(--background-color);
}
*:deep(.v-list-item) {
    min-height: 32px !important;
    padding: 0 8px !important;
}
</style>
