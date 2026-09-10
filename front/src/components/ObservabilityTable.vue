<template>
    <div class="observability-table-wrap">
        <v-simple-table v-if="loading || items.length" dense class="observability-table">
            <thead>
                <tr>
                    <th v-for="header in headers" :key="headerKey(header)" class="px-2" :class="header.headerClass">
                        {{ headerLabel(header) }}
                    </th>
                </tr>
            </thead>
            <tbody :class="{ mono }">
                <template v-for="(item, itemIndex) in items">
                    <router-link
                        v-if="rowLink"
                        :key="rowKeyValue(item, itemIndex)"
                        :to="rowLink(item)"
                        tag="tr"
                        exact
                        class="observability-row clickable"
                        :class="rowClassValue(item)"
                    >
                        <td
                            v-for="(header, columnIndex) in headers"
                            :key="headerKey(header)"
                            class="text-no-wrap px-2"
                            :class="[{ 'pl-0': columnIndex === 0 }, cellClassValue(header, item)]"
                        >
                            <div v-if="columnIndex === 0 && rowColor" class="leading-cell">
                                <div class="marker" :style="{ backgroundColor: rowColor(item) }" />
                                <slot :name="`item.${headerKey(header)}`" :item="item" :header="header" :value="itemValue(item, header)">
                                    {{ itemValue(item, header) }}
                                </slot>
                            </div>
                            <slot v-else :name="`item.${headerKey(header)}`" :item="item" :header="header" :value="itemValue(item, header)">
                                {{ itemValue(item, header) }}
                            </slot>
                        </td>
                    </router-link>
                    <tr
                        v-else
                        :key="rowKeyValue(item, itemIndex)"
                        class="observability-row"
                        :class="[{ clickable }, rowClassValue(item)]"
                        :tabindex="clickable ? 0 : null"
                        @click="selectRow(item)"
                        @keydown.enter="selectRow(item)"
                    >
                        <td
                            v-for="(header, columnIndex) in headers"
                            :key="headerKey(header)"
                            class="text-no-wrap px-2"
                            :class="[{ 'pl-0': columnIndex === 0 }, cellClassValue(header, item)]"
                        >
                            <div v-if="columnIndex === 0 && rowColor" class="leading-cell">
                                <div class="marker" :style="{ backgroundColor: rowColor(item) }" />
                                <slot :name="`item.${headerKey(header)}`" :item="item" :header="header" :value="itemValue(item, header)">
                                    {{ itemValue(item, header) }}
                                </slot>
                            </div>
                            <slot v-else :name="`item.${headerKey(header)}`" :item="item" :header="header" :value="itemValue(item, header)">
                                {{ itemValue(item, header) }}
                            </slot>
                        </td>
                    </tr>
                </template>
            </tbody>
        </v-simple-table>
        <div v-else class="pa-3 text-center grey--text">{{ emptyText }}</div>
    </div>
</template>

<script>
export default {
    props: {
        headers: {
            type: Array,
            required: true,
        },
        items: {
            type: Array,
            default: () => [],
        },
        loading: Boolean,
        emptyText: {
            type: String,
            default: 'No results found',
        },
        mono: Boolean,
        clickable: Boolean,
        rowKey: {
            type: [String, Function],
            default: '',
        },
        rowLink: Function,
        rowClass: {
            type: [String, Object, Function],
            default: '',
        },
        rowColor: Function,
        valueGetter: Function,
    },

    methods: {
        headerKey(header) {
            return header.value || header.key;
        },
        headerLabel(header) {
            return header.text || header.label;
        },
        itemValue(item, header) {
            return this.valueGetter ? this.valueGetter(item, header) : item[this.headerKey(header)];
        },
        cellClassValue(header, item) {
            return typeof header.cellClass === 'function' ? header.cellClass(item) : header.cellClass;
        },
        rowKeyValue(item, index) {
            if (typeof this.rowKey === 'function') {
                return this.rowKey(item, index);
            }
            return (this.rowKey && item[this.rowKey]) || index;
        },
        rowClassValue(item) {
            return typeof this.rowClass === 'function' ? this.rowClass(item) : this.rowClass;
        },
        selectRow(item) {
            if (this.clickable) {
                this.$emit('row-click', item);
            }
        },
    },
};
</script>

<style scoped>
.observability-table-wrap,
.observability-table {
    width: 100%;
}
.mono {
    font-family: monospace, monospace;
}
.leading-cell {
    display: flex;
    align-items: center;
    gap: 4px;
}
.marker {
    width: 4px;
    height: 20px;
    flex: 0 0 4px;
    filter: brightness(var(--brightness));
}
.clickable {
    cursor: pointer;
}
.clickable:focus-visible {
    outline: 2px solid var(--primary-color, #1976d2);
    outline-offset: -2px;
}
</style>
