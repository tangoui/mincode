import test from 'node:test';
import assert from 'node:assert/strict';
import { describePluginActionFailure } from '../src/domain/plugin-ui/action-error.mjs';

// grpc-debug is now a Go process plugin; the host keeps mapping protocol failures.
test('host maps protocol failures to actionable diagnostics', () => {
  const text = describePluginActionFailure(new Error('context deadline exceeded'), 'fallback');
  assert.notEqual(text, 'fallback');
});
