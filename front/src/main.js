import Vue from 'vue';
import VueRouter from 'vue-router';
import vuetify from '@/plugins/vuetify';
import '@/plugins/resize';
import '@/plugins/highlight';
import pluralize from 'pluralize';
import events from '@/utils/events';
import Utils from '@/utils/utils';
import * as validators from '@/utils/validators';
import * as storage from '@/utils/storage';
import * as format from '@/utils/format';
import Api from '@/api';
import App from '@/App';
import Project from '@/views/Project';
import Overview from '@/views/Overview';
import Login from '@/views/auth/Login.vue';
import Logout from '@/views/auth/Logout.vue';
import Saml from '@/views/auth/Saml.vue';
import MCPConsent from '@/views/auth/MCPConsent.vue';

Vue.config.productionTip = false;
Vue.config.devtools = false;

const config = window.coroot;

Vue.use(VueRouter);
const router = new VueRouter({
    mode: 'history',
    base: config.base_path,
    routes: [
        { path: '/login', name: 'login', component: Login, meta: { anonymous: true } },
        { path: '/logout', name: 'logout', component: Logout, meta: { anonymous: true } },
        { path: '/sso/saml', name: 'saml', component: Saml, meta: { anonymous: true } },
        { path: '/auth/mcp-consent', name: 'mcp-consent', component: MCPConsent },
        { path: '/p/settings/:tab?', name: 'project_new', component: Project, props: true },
        { path: '/p/:projectId/settings/:tab?', name: 'project_settings', component: Project, props: true },
        {
            path: '/p/:projectId/:view?/:id?/:report?',
            name: 'overview',
            component: Overview,
            props: true,
        },
        { path: '/', name: 'index', component: App },
        { path: '*', redirect: { name: 'index' } },
    ],
    scrollBehavior(to) {
        if (to.hash) {
            try {
                document.querySelector(to.hash);
                return new Promise((resolve) => {
                    setTimeout(() => {
                        resolve({ selector: to.hash, behavior: 'smooth' });
                    }, 300);
                });
            } catch {
                //
            }
        }
    },
});

const api = new Api(router, config.base_path);

Vue.prototype.$events = events;
Vue.prototype.$format = format;
Vue.prototype.$pluralize = pluralize;
Vue.prototype.$api = api;
Vue.prototype.$utils = new Utils(router);
Vue.prototype.$validators = validators;
Vue.prototype.$storage = storage;
Vue.prototype.$coroot = config;

new Vue({
    router,
    vuetify,
    render: (h) => h(App),
}).$mount('#app');
