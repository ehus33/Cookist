import { useState } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it } from 'vitest';
import { DynamicEditor } from './DynamicEditor';
import { defaultDynamicCollection, type DynamicConfig } from './dynamicApps';

afterEach(cleanup);
function Editor({ editing = false, initial, disabled = false }: { editing?: boolean; initial?: DynamicConfig; disabled?: boolean }) {
  const [value, setValue] = useState(initial);
  return <form><DynamicEditor value={value} onChange={setValue} disabled={disabled} editing={editing} /></form>;
}

it('enables useful defaults, configures fields/access and validates explicit collection IDs without nesting forms', () => {
  const { container } = render(<Editor />);
  fireEvent.click(screen.getByLabelText('Enable data and member accounts'));
  expect(screen.getByLabelText('Collection 1 ID')).toHaveValue('requests');
  expect(screen.getByLabelText('Collection 1 field 1 required')).toBeChecked();
  expect(screen.getByLabelText('Collection 1 field 2 required')).not.toBeChecked();
  fireEvent.change(screen.getByLabelText('Collection 1 read access'), { target: { value: 'public' } });
  expect(screen.getByLabelText('Collection 1 read access')).toHaveValue('public');
  fireEvent.click(screen.getByRole('button', { name: 'Add collection' }));
  expect(screen.getByRole('alert')).toHaveTextContent('Collection 2 ID');
  fireEvent.change(screen.getByLabelText('Collection 2 ID'), { target: { value: 'contacts' } });
  fireEvent.change(screen.getByLabelText('Collection 2 name'), { target: { value: 'Contacts' } });
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Add field to collection 2' }));
  expect(screen.getByRole('alert')).toHaveTextContent('field 3 ID');
  fireEvent.click(screen.getByRole('button', { name: 'Remove field 3 from collection 2' }));
  fireEvent.click(screen.getByRole('button', { name: 'Remove collection 2' }));
  fireEvent.click(screen.getByLabelText('Enable data and member accounts'));
  expect(screen.queryByLabelText('Collection 1 ID')).not.toBeInTheDocument();
  expect(container.querySelectorAll('form')).toHaveLength(1);
});

it('locks all existing permissions/schema and disable toggle on edits but allows appending collections', () => {
  render(<Editor editing initial={{ collections: [defaultDynamicCollection()] }} />);
  expect(screen.getByLabelText('Enable data and member accounts')).toBeDisabled();
  expect(screen.getByLabelText('Collection 1 ID')).toBeDisabled();
  expect(screen.getByLabelText('Collection 1 read access')).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Remove collection 1' })).toBeDisabled();
  fireEvent.click(screen.getByRole('button', { name: 'Add collection' }));
  expect(screen.getByLabelText('Collection 2 ID')).toBeEnabled();
  expect(screen.getByLabelText('Collection 1 field 1 label')).toBeDisabled();
});

it('disables all changes while deployment is busy', () => {
  render(<Editor disabled initial={{ collections: [defaultDynamicCollection()] }} />);
  expect(screen.getByRole('button', { name: 'Add collection' })).toBeDisabled();
  expect(screen.getByLabelText('Collection 1 ID')).toBeDisabled();
});
