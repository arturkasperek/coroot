'use strict';

function emit(severityText, body, attributes, isError) {
  const line = JSON.stringify({ severityText, body, ...(attributes || {}) });
  if (isError) {
    console.error(line);
  } else {
    console.log(line);
  }
}

module.exports = {
  info: (body, attributes) => emit('INFO', body, attributes, false),
  error: (body, attributes) => emit('ERROR', body, attributes, true),
};
