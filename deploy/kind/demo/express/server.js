'use strict';

require('./otel');

const express = require('express');
const log = require('./log');

const app = express();
const port = Number(process.env.PORT || 3000);

app.get('/health', (_req, res) => {
  res.status(200).send('ok');
});

app.get('/api/hello', (req, res) => {
  log.info('express hello', { path: req.path });
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
