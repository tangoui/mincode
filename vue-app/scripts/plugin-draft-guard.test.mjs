import test from 'node:test';
import assert from 'node:assert/strict';
import { createDraftGuard } from '../src/plugin-ui/draft-guard.mjs';

test('the draft guard parks draft-dropping actions until the user discards the draft', () => {
  let dirty = true, asked = 0;
  const log = [];
  const guard = createDraftGuard({ dirty: () => dirty, ask: () => { asked++; }, discard: () => { dirty = false; log.push('discard'); } });
  const disconnect = () => { if (!guard.allows(disconnect)) return; log.push('disconnect'); };
  disconnect();
  assert.deepEqual([asked, log], [1, []], 'a dirty draft blocks and asks');
  guard.discardAndContinue();
  assert.deepEqual(log, ['discard', 'disconnect'], 'confirming discards first, then resumes the parked action');
  disconnect();
  assert.deepEqual([asked, log.at(-1)], [1, 'disconnect'], 'a clean editor never asks');
  dirty = true; guard.allows(() => log.push('first')); guard.allows(() => log.push('second')); guard.discardAndContinue();
  assert.equal(log.at(-1), 'second', 'only the latest parked action resumes');
});

test('discarding with nothing parked only discards, and an action resumes at most once', () => {
  let dirty = true, resumed = 0;
  const log = [];
  const guard = createDraftGuard({ dirty: () => dirty, ask: () => {}, discard: () => { dirty = false; log.push('discard'); } });
  guard.discardAndContinue();
  assert.deepEqual(log, ['discard'], 'nothing parked: only discards');
  dirty = true;
  guard.allows(() => { resumed++; });
  guard.discardAndContinue();
  guard.discardAndContinue();
  assert.equal(resumed, 1, 'the parked action is cleared once it resumes');
});
