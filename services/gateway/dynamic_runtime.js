(() => {
  'use strict';
  const base = document.currentScript.dataset.appBase;
  const widgets = [...document.querySelectorAll('[data-dynamic]')];
  const storageKey = 'cookist-member-' + base;
  let session;
  let member;
  let collections = [];
  let revision = 0;
  const object = value => !!value && typeof value === 'object' && !Array.isArray(value);
  const identifier = value => typeof value === 'string' && /^[a-z][a-z0-9_-]{0,31}$/.test(value);
  const recordID = value => typeof value === 'string' && /^[a-f0-9]{32}$/.test(value);
  function validCollection(value) {
    return object(value) && identifier(value.id) && typeof value.name === 'string'
      && ['public', 'members', 'own'].includes(value.read)
      && ['members', 'none'].includes(value.create) && ['own', 'none'].includes(value.update)
      && ['own', 'none'].includes(value.delete) && Array.isArray(value.fields)
      && value.fields.length > 0 && value.fields.length <= 12 && value.fields.every(field =>
        object(field) && identifier(field.id) && typeof field.label === 'string'
        && ['text', 'email', 'number', 'boolean'].includes(field.type) && typeof field.required === 'boolean');
  }
  function validRecord(value) {
    return object(value) && recordID(value.id) && object(value.values)
      && Object.values(value.values).every(item => typeof item === 'string' || typeof item === 'boolean'
        || (typeof item === 'number' && Number.isFinite(item)))
      && Number.isInteger(value.version) && value.version >= 1
      && typeof value.createdAt === 'string' && typeof value.updatedAt === 'string'
      && (value.canUpdate === undefined || typeof value.canUpdate === 'boolean')
      && (value.canDelete === undefined || typeof value.canDelete === 'boolean');
  }
  function node(tag, text) {
    const result = document.createElement(tag);
    if (text !== undefined) result.textContent = text;
    return result;
  }
  function button(text, handler) {
    const result = node('button', text);
    result.type = 'button';
    result.className = 'page-button';
    result.addEventListener('click', handler);
    return result;
  }
  function status(widget, text, error = false) {
    const target = widget.querySelector('.dynamic-status');
    target.textContent = text;
    target.setAttribute('role', error ? 'alert' : 'status');
  }
  function clearSession() {
    session = undefined;
    member = undefined;
    sessionStorage.removeItem(storageKey);
  }
  async function request(path, method = 'GET', body, version) {
    const headers = { Accept: 'application/json' };
    if (session) headers.Authorization = 'Bearer ' + session.accessToken;
    if (body) headers['Content-Type'] = 'application/json';
    if (version !== undefined) headers['If-Match'] = String(version);
    const response = await fetch(base + path, {
      method, headers, body: body ? JSON.stringify(body) : undefined,
      credentials: 'omit', signal: AbortSignal.timeout(12000),
    });
    const data = await response.json();
    if (!response.ok) {
      if (response.status === 401) {
        clearSession();
        revision++;
        widgets.forEach(widget => {
          if (widget.dataset.dynamic !== 'member-account') widget.querySelector('.dynamic-body').replaceChildren();
        });
        widgets.filter(widget => widget.dataset.dynamic === 'member-account').forEach(widget => renderAccount(widget));
      }
      throw new Error(object(data) && object(data.error) && typeof data.error.message === 'string'
        ? data.error.message : 'The app service returned an unexpected error.');
    }
    return data;
  }
  function fieldInputs(form, fields, values = {}) {
    const inputs = new Map();
    for (const field of fields) {
      const label = node('label', field.label + (field.required ? ' *' : ''));
      const input = node('input');
      input.name = field.id;
      input.type = field.type === 'boolean' ? 'checkbox' : field.type === 'number' ? 'number' : field.type === 'email' ? 'email' : 'text';
      input.required = field.required && field.type !== 'boolean';
      if (field.type === 'number') input.step = 'any';
      if (field.type === 'text' || field.type === 'email') input.maxLength = 2000;
      if (field.type === 'boolean') input.checked = values[field.id] === true;
      else input.value = values[field.id] === undefined ? '' : String(values[field.id]);
      label.append(input);
      form.append(label);
      inputs.set(field.id, input);
    }
    return () => {
      const values = {};
      for (const field of fields) {
        const input = inputs.get(field.id);
        if (field.type === 'boolean') values[field.id] = input.checked;
        else if (input.value !== '') values[field.id] = field.type === 'number' ? Number(input.value) : input.value;
      }
      return values;
    };
  }
  function submitForm(widget, form, text, action) {
    const submit = node('button', text);
    submit.type = 'submit';
    submit.className = 'page-button';
    form.append(submit);
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (submit.disabled || !form.reportValidity()) return;
      submit.disabled = true;
      status(widget, 'Working...');
      try { await action(); }
      catch (error) { status(widget, error instanceof Error ? error.message : 'Request failed. Please try again.', true); }
      finally { submit.disabled = false; }
    });
  }
  function accountField(form, labelText, type, autocomplete) {
    const label = node('label', labelText);
    const input = node('input');
    input.type = type;
    input.required = true;
    input.autocomplete = autocomplete;
    input.maxLength = type === 'password' ? 256 : 254;
    label.append(input);
    form.append(label);
    return input;
  }
  function renderAccount(widget, mode = 'login', rememberedEmail = '') {
    const body = widget.querySelector('.dynamic-body');
    body.replaceChildren();
    status(widget, '');
    if (member) {
      body.append(node('p', 'You are signed in to this app.'));
      body.append(button('Sign out', async event => {
        const control = event.currentTarget;
        control.disabled = true;
        try {
          const result = await request('/auth/logout', 'POST', {});
          if (!object(result) || result.signedOut !== true) throw new Error('The server did not confirm sign out.');
          clearSession();
          await refresh();
        } catch (error) {
          status(widget, error instanceof Error ? error.message : 'Sign out failed.', true);
        } finally { control.disabled = false; }
      }));
      return;
    }
    const form = node('form');
    form.className = 'dynamic-form';
    const email = accountField(form, 'Email', 'email', 'email');
    email.value = rememberedEmail;
    let code;
    let password;
    if (mode === 'confirm' || mode === 'reset') code = accountField(form, 'Confirmation code', 'text', 'one-time-code');
    if (mode === 'login' || mode === 'signup' || mode === 'reset') password = accountField(form, 'Password', 'password', mode === 'login' ? 'current-password' : 'new-password');
    if (mode === 'signup' || mode === 'reset') {
      password.minLength = 12;
      form.append(node('p', 'Use at least 12 characters, including uppercase, lowercase, a number and a symbol.'));
    }
    const titles = { login: 'Sign in', signup: 'Create account', confirm: 'Confirm email', forgot: 'Send reset code', reset: 'Reset password' };
    submitForm(widget, form, titles[mode], async () => {
      const payload = { email: email.value };
      if (password) payload.password = password.value;
      if (code) payload.code = code.value;
      const result = await request('/auth/' + mode, 'POST', payload);
      if (mode === 'login') {
        if (!object(result) || typeof result.accessToken !== 'string' || !result.accessToken
          || !Number.isInteger(result.expiresIn) || result.expiresIn <= 0) throw new Error('The server returned an invalid sign-in response.');
        session = { accessToken: result.accessToken, expiresAt: Date.now() + result.expiresIn * 1000 };
        const identity = await request('/me');
        if (!object(identity) || typeof identity.id !== 'string' || !identity.id) {
          clearSession();
          throw new Error('The server returned an invalid member identity.');
        }
        member = identity.id;
        sessionStorage.setItem(storageKey, JSON.stringify(session));
        await refresh();
      } else if (mode === 'signup' || mode === 'forgot') {
        if (!object(result) || result.confirmationRequired !== true) throw new Error('The server did not confirm that a verification code is required.');
        renderAccount(widget, mode === 'signup' ? 'confirm' : 'reset', email.value);
        status(widget, 'Check your email for a confirmation code.');
      } else {
        if (!object(result) || (mode === 'confirm' ? result.confirmed !== true : result.reset !== true)) throw new Error('The server did not confirm this action.');
        renderAccount(widget, 'login', email.value);
        status(widget, mode === 'confirm' ? 'Email confirmed. Sign in to continue.' : 'Password reset. Sign in to continue.');
      }
    });
    body.append(form);
    if (mode !== 'login') body.append(button('Back to sign in', () => renderAccount(widget)));
    if (mode === 'login') {
      body.append(button('Create account', () => renderAccount(widget, 'signup')));
      body.append(button('Confirm email', () => renderAccount(widget, 'confirm')));
      body.append(button('Forgot password', () => renderAccount(widget, 'forgot')));
    }
  }
  function collectionPath(collection, id) {
    return '/collections/' + collection.id + '/records' + (id ? '/' + id : '');
  }
  async function renderList(widget, collection, cursor) {
    const version = revision;
    const body = widget.querySelector('.dynamic-body');
    body.replaceChildren();
    status(widget, 'Loading records...');
    if (!member && collection.read !== 'public') {
      status(widget, 'Sign in using the member account element to view records.');
      return;
    }
    try {
      const result = await request(collectionPath(collection) + (cursor ? '?cursor=' + encodeURIComponent(cursor) : ''));
      if (!object(result) || !Array.isArray(result.records) || !result.records.every(validRecord)
        || (result.next !== undefined && typeof result.next !== 'string')) throw new Error('The server returned an invalid record list.');
      if (version !== revision) return;
      const list = node('ul');
      list.className = 'dynamic-records';
      for (const record of result.records) {
        const item = node('li');
        const title = collection.fields.find(field => record.values[field.id] !== undefined);
        item.append(node('strong', title ? String(record.values[title.id]) : record.id));
        item.append(button('View details', () => {
          location.hash = encodeURIComponent(collection.id) + '/' + record.id;
          void renderDetails(widget, collection, record.id);
        }));
        list.append(item);
      }
      body.append(list);
      if (result.next) body.append(button('Next records', () => void renderList(widget, collection, result.next)));
      if (cursor) body.append(button('First records', () => void renderList(widget, collection)));
      body.append(button('Refresh records', () => void renderList(widget, collection)));
      status(widget, result.records.length ? '' : 'No records yet.');
    } catch (error) {
      if (version === revision || !session) {
        status(widget, error instanceof Error ? error.message : 'Unable to load records.', true);
        body.append(button('Retry records', () => void renderList(widget, collection, cursor)));
      }
    }
  }
  async function renderDetails(widget, collection, id) {
    const version = revision;
    const body = widget.querySelector('.dynamic-body');
    body.replaceChildren();
    status(widget, 'Loading record...');
    try {
      const result = await request(collectionPath(collection, id));
      if (!object(result) || !validRecord(result.record)) throw new Error('The server returned an invalid record.');
      if (version !== revision) return;
      const record = result.record;
      const details = node('dl');
      for (const field of collection.fields) {
        details.append(node('dt', field.label), node('dd', record.values[field.id] === undefined ? 'Not provided' : String(record.values[field.id])));
      }
      body.append(details);
      body.append(button('Back to records', () => { history.replaceState(null, '', location.pathname + location.search); void renderList(widget, collection); }));
      if (record.canUpdate === true) body.append(button('Edit record', () => {
        body.replaceChildren();
        const form = node('form');
        form.className = 'dynamic-form';
        const values = fieldInputs(form, collection.fields, record.values);
        submitForm(widget, form, 'Save changes', async () => {
          const result = await request(collectionPath(collection, id), 'PATCH', { values: values(), version: record.version });
          if (!object(result) || !validRecord(result.record)) throw new Error('The server did not confirm a saved record.');
          await renderDetails(widget, collection, id);
        });
        body.append(form, button('Cancel edit', () => void renderDetails(widget, collection, id)));
      }));
      if (record.canDelete === true) body.append(button('Delete record', () => {
        const confirmation = node('div');
        confirmation.append(node('p', 'Permanently delete this record?'));
        confirmation.append(button('Confirm delete', async event => {
          event.currentTarget.disabled = true;
          try {
            const result = await request(collectionPath(collection, id), 'DELETE', undefined, record.version);
            if (!object(result) || result.deleted !== true) throw new Error('The server did not confirm deletion.');
            history.replaceState(null, '', location.pathname + location.search);
            await renderList(widget, collection);
          } catch (error) { status(widget, error instanceof Error ? error.message : 'Deletion failed.', true); }
        }));
        confirmation.append(button('Cancel deletion', () => confirmation.remove()));
        body.append(confirmation);
      }));
      status(widget, '');
    } catch (error) {
      if (version === revision || !session) {
        status(widget, error instanceof Error ? error.message : 'Unable to load record.', true);
        body.append(button('Retry record', () => void renderDetails(widget, collection, id)));
        body.append(button('Back to records', () => { history.replaceState(null, '', location.pathname + location.search); void renderList(widget, collection); }));
      }
    }
  }
  function renderConnectedForm(widget, collection) {
    const body = widget.querySelector('.dynamic-body');
    body.replaceChildren();
    if (!member || collection.create !== 'members') {
      status(widget, collection.create === 'none' ? 'Visitors cannot create records in this collection.' : 'Sign in using the member account element to create records.');
      return;
    }
    const form = node('form');
    form.className = 'dynamic-form';
    const values = fieldInputs(form, collection.fields);
    submitForm(widget, form, 'Save record', async () => {
      const result = await request(collectionPath(collection), 'POST', { values: values() });
      if (!object(result) || !validRecord(result.record)) throw new Error('The server did not confirm a saved record.');
      form.reset();
      await refresh();
      status(widget, 'Record saved.');
    });
    body.append(form);
    status(widget, '');
  }
  async function refresh() {
    revision++;
    const detail = location.hash.slice(1).split('/');
    await Promise.all(widgets.map(async widget => {
      if (widget.dataset.dynamic === 'member-account') { renderAccount(widget); return; }
      const collection = collections.find(item => item.id === widget.dataset.collection);
      if (!collection) { status(widget, 'This element has no available data collection.', true); return; }
      if (widget.dataset.dynamic === 'data-form') renderConnectedForm(widget, collection);
      else if (detail[0] === collection.id && recordID(detail[1])) await renderDetails(widget, collection, detail[1]);
      else await renderList(widget, collection);
    }));
  }
  async function start() {
    try {
      const saved = sessionStorage.getItem(storageKey);
      if (saved) {
        let parsed;
        try { parsed = JSON.parse(saved); }
        catch {
          clearSession();
          throw new Error('Your saved app session is invalid. Retry to sign in again.');
        }
        if (!object(parsed) || typeof parsed.accessToken !== 'string' || typeof parsed.expiresAt !== 'number') {
          clearSession();
          throw new Error('Your saved app session is invalid. Reload and sign in again.');
        }
        if (parsed.expiresAt > Date.now()) session = parsed;
        else clearSession();
      }
      const config = await request('/config');
      if (!object(config) || config.authentication !== true || !Array.isArray(config.collections)
        || !config.collections.length || config.collections.length > 8 || !config.collections.every(validCollection)) throw new Error('The server returned invalid app configuration.');
      collections = config.collections;
      if (session) {
        const identity = await request('/me');
        if (!object(identity) || typeof identity.id !== 'string' || !identity.id) throw new Error('The server returned an invalid member identity.');
        member = identity.id;
      }
      await refresh();
    } catch (error) {
      widgets.forEach(widget => {
        const body = widget.querySelector('.dynamic-body');
        body.replaceChildren(button('Retry loading app', () => { void start(); }));
        status(widget, error instanceof Error ? error.message : 'Unable to start this app.', true);
      });
    }
  }
  window.addEventListener('hashchange', () => { void refresh(); });
  void start();
})();
