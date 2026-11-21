import test from 'node:test';
import assert from 'node:assert/strict';
import { Window } from 'happy-dom';

// The shim reads DOM classes from globals, as it does inside the plugin iframe.
const window = new Window();
for (const name of ['Element', 'HTMLInputElement', 'HTMLButtonElement', 'SubmitEvent']) globalThis[name] = window[name];
const { installSandboxForms } = await import('../src/plugin-ui/sandbox-forms.js');
const document = window.document;
installSandboxForms(document);

function form(html) {
  document.body.innerHTML = `<form>${html}</form>`;
  const el = document.querySelector('form');
  const submits = [];
  el.addEventListener('submit', event => { event.preventDefault(); submits.push(event.submitter?.textContent ?? null); });
  return { el, submits };
}
const key = (target, init = {}) => {
  const event = new window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
};

test('a submit button dispatches one cancelable submit event and cancels the native submission', () => {
  const { el, submits } = form('<input><button type="submit">Save</button><button type="button">Cancel</button>');
  const click = new window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 });
  el.querySelector('button').dispatchEvent(click);
  assert.deepEqual(submits, ['Save']);
  assert.equal(click.defaultPrevented, true);
  el.querySelectorAll('button')[1].dispatchEvent(new window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  assert.equal(submits.length, 1, 'type=button does not submit');
});

test('disabled buttons and handled clicks do not submit', () => {
  const { el, submits } = form('<button type="submit" disabled>Save</button>');
  el.querySelector('button').dispatchEvent(new window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  const { el: other, submits: otherSubmits } = form('<button type="submit">Save</button>');
  other.querySelector('button').addEventListener('click', event => event.preventDefault());
  other.querySelector('button').dispatchEvent(new window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  assert.deepEqual([submits, otherSubmits], [[], []]);
});

test('Enter in a field submits like implicit submission', () => {
  const { el, submits } = form('<input><input><button type="submit">Go</button>');
  const event = key(el.querySelector('input'));
  assert.deepEqual(submits, ['Go']);
  assert.equal(event.defaultPrevented, true);
  key(el.querySelector('input'), { shiftKey: true });
  assert.equal(submits.length, 1, 'modified Enter is left alone');
  const select = el.querySelectorAll('input')[1];
  select.addEventListener('keydown', e => e.preventDefault()); // e.g. a filterable select creating an option
  key(select);
  assert.equal(submits.length, 1, 'components that consume Enter keep it');
});

test('without a submit button only a lone text field submits', () => {
  const lone = form('<input>');
  key(lone.el.querySelector('input'));
  assert.deepEqual(lone.submits, [null]);
  const two = form('<input><input>');
  key(two.el.querySelector('input'));
  assert.deepEqual(two.submits, []);
  const area = form('<textarea></textarea><button type="submit">Go</button>');
  key(area.el.querySelector('textarea'));
  assert.deepEqual(area.submits, [], 'Enter in a textarea is a newline');
});

test('buttons inside a container that stops click propagation still submit', () => {
  const { el, submits } = form('<div class="overlay"><button type="submit">Save</button></div>');
  el.querySelector('.overlay').addEventListener('click', event => event.stopPropagation());
  el.querySelector('button').dispatchEvent(new window.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  assert.deepEqual(submits, ['Save']);
});
