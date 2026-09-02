'use strict';

require('./otel');

const { metrics } = require('@opentelemetry/api');
const express = require('express');
const log = require('./log');

const app = express();
const port = Number(process.env.PORT || 3000);
const requests = metrics.getMeter('express-demo').createCounter('express_demo_requests_total', {
  description: 'HTTP requests handled by express-demo',
});

app.use((req, res, next) => {
  res.on('finish', () => {
    requests.add(1, { method: req.method, route: req.path, status: String(res.statusCode) });
  });
  next();
});

app.get('/health', (_req, res) => {
  res.status(200).send('ok');
});

app.get('/api/hello', (req, res) => {
  log.info(req.query.token ? `express hello ${req.query.token}` : 'express hello', { path: req.path });
  res.json({ service: 'express-demo', message: 'hello from express', ts: Date.now() });
});

app.get('/api/slow', (_req, res) => {
  log.info('express slow path');
  setTimeout(() => {
    res.json({ service: 'express-demo', slow: true });
  }, 250);
});

app.get('/api/error', (_req, res) => {
  log.error('express simulated failure');
  res.status(500).json({ service: 'express-demo', error: 'simulated' });
});

app.listen(port, '0.0.0.0', () => {
  log.info('express-demo listening', { port: String(port) });
});
