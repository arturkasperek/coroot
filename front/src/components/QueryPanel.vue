<template>
    <v-card outlined class="query-panel px-4 pt-2 pb-4 mb-2">
        <div class="subtitle-1">Query:</div>
        <div class="d-flex flex-wrap flex-md-nowrap gap-2">
            <QueryBuilder
                :value="value"
                :loading="optionsLoading"
                :items="items"
                :error="error"
                :disabled="disabled"
                :hidden-attributes="hiddenAttributes"
                :allow-free-text="allowFreeText"
                @input="$emit('input', $event)"
                @get="get"
                class="flex-grow-1"
            />
            <slot name="actions" />
        </div>
        <slot />
        <v-progress-linear v-if="loading" indeterminate height="4" class="progress" />
    </v-card>
</template>

<script>
import QueryBuilder from '@/components/QueryBuilder.vue';

export default {
    components: { QueryBuilder },

    props: {
        value: {
            type: Array,
            default: () => [],
        },
        items: {
            type: Array,
            default: () => [],
        },
        optionsLoading: Boolean,
        loading: Boolean,
        error: String,
        disabled: Boolean,
        hiddenAttributes: {
            type: Array,
            default: () => [],
        },
        allowFreeText: {
            type: Boolean,
            default: true,
        },
    },

    methods: {
        get(what, name) {
            this.$emit('get', what, name);
        },
    },
};
</script>

<style scoped>
.query-panel {
    position: relative;
}
.progress {
    position: absolute;
    bottom: 0;
    left: 0;
}
</style>
