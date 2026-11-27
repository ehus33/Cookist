import { Fragment, useRef, useState } from 'react';
import type { DragEvent, ReactNode } from 'react';
import { blockRows, blockTypes, insertBlock, isSafeWebURL, moveBlock, newBlock, placeBeside, removeBlock, splitRow } from './pageBlocks';
import type { BlockType, PageBlock } from './pageBlocks';

const dragFormat = 'application/x-cookist-element';
const elementIcons: Record<BlockType, ReactNode> = {
  heading: <path d="M5 5v14M19 5v14M5 12h14M3 5h4M17 5h4M3 19h4M17 19h4" />,
  text: <path d="M4 5h16M4 10h16M4 15h16M4 20h10" />,
  image: <><rect x="3" y="3" width="18" height="18" rx="2" /><circle cx="8" cy="8" r="1.5" /><path d="m3 17 5-5 4 4 4-6 5 7" /></>,
  button: <><rect x="3" y="4" width="18" height="10" rx="3" /><path d="M7 9h7m0 4 2 8 2-3 3-1-7-4Z" /></>,
  divider: <><path d="M3 12h18" /><path d="M5 5h14M5 19h14" strokeDasharray="2 3" /></>,
  spacer: <path d="M4 3h16M4 21h16M12 6v12m-3-9 3-3 3 3m-6 6 3 3 3-3" />,
  products: <><path d="M5 7h14l2 14H3L5 7Z" /><path d="M8 9V6a4 4 0 0 1 8 0v3" /></>,
};

export function SiteEditor({ blocks, onChange, style, disabled, catalog }: {
  blocks: PageBlock[];
  onChange: (blocks: PageBlock[]) => void;
  style: string;
  disabled: boolean;
  catalog: ReactNode;
}) {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mobile, setMobile] = useState(false);
  const [preview, setPreview] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [placement, setPlacement] = useState<'row' | 'left' | 'right'>('row');
  const [history, setHistory] = useState<{ past: PageBlock[][]; future: PageBlock[][] }>({ past: [], future: [] });
  const drag = useRef<{ type?: BlockType; id?: string } | null>(null);
  const selected = blocks.find(block => block.id === selectedId);
  const rows = blockRows(blocks);
  const selectedRow = rows.find(row => row.some(block => block.id === selectedId));
  const canInsertBeside = selectedRow?.length === 1;
  const selectedIndex = blocks.findIndex(block => block.id === selectedId);
  const previous = blocks[selectedIndex - 1];
  const canPairPrevious = !!selected && !!previous && !selected.beside && !previous.beside;

  function commit(next: PageBlock[], message?: string) {
    if (disabled || next === blocks || JSON.stringify(next) === JSON.stringify(blocks)) return;
    setHistory(current => ({ past: [...current.past.slice(-49), blocks], future: [] }));
    onChange(next);
    if (message) setAnnouncement(message);
  }

  function add(type: BlockType, index = blocks.length, beside?: { id: string; side: 'left' | 'right' }) {
    if (blocks.length >= 40 || (type === 'products' && blocks.some(block => block.type === 'products'))) return;
    const block = newBlock(type);
    const next = beside ? placeBeside(blocks, block, beside.id, beside.side) : insertBlock(blocks, block, index);
    if (next === blocks) return;
    commit(next, `${blockTypes.find(item => item.type === type)?.label} added at position ${index + 1}.`);
    setSelectedId(block.id);
  }

  function startDrag(event: DragEvent, value: { type?: BlockType; id?: string }) {
    if (disabled || preview) { event.preventDefault(); return; }
    drag.current = value;
    event.dataTransfer.effectAllowed = value.id ? 'move' : 'copy';
    event.dataTransfer.setData(dragFormat, JSON.stringify(value));
  }

  function drop(event: DragEvent, index: number, beside?: { id: string; side: 'left' | 'right' }) {
    event.preventDefault();
    event.stopPropagation();
    const value = drag.current;
    drag.current = null;
    setDropTarget(null);
    if (disabled || preview || !value) return;
    if (value.id) {
      const block = blocks.find(item => item.id === value.id);
      if (block) commit(beside ? placeBeside(blocks, block, beside.id, beside.side) : moveBlock(blocks, value.id, index), 'Element moved.');
    } else if (value.type) add(value.type, index, beside);
  }

  function change(field: 'text' | 'url' | 'alt' | 'align' | 'size', value: string) {
    if (!selected) return;
    const next = { ...selected };
    if (field === 'align') {
      if (value !== 'left' && value !== 'center' && value !== 'right') return;
      next.align = value;
    } else if (field === 'size') {
      if (value !== 'small' && value !== 'medium' && value !== 'large') return;
      next.size = value;
    } else next[field] = value;
    commit(blocks.map(block => block.id === selected.id ? next : block));
  }

  function undo(redo: boolean) {
    const source = redo ? history.future : history.past;
    const next = source.at(-1);
    if (!next || disabled) return;
    setHistory(redo
      ? { past: [...history.past, blocks], future: history.future.slice(0, -1) }
      : { past: history.past.slice(0, -1), future: [...history.future, blocks] });
    onChange(next);
    setAnnouncement(redo ? 'Change redone.' : 'Change undone.');
  }

  function renderBlock(block: PageBlock) {
    switch (block.type) {
      case 'heading': return <h2>{block.text}</h2>;
      case 'text': return <p>{block.text}</p>;
      case 'image': return isSafeWebURL(block.url) ? <img src={block.url} alt={block.alt} loading="lazy" referrerPolicy="no-referrer" />
        : <div className="builder-image-placeholder">Image: add an HTTPS URL and alternative text</div>;
      case 'button': return <span className="page-button">{block.text}</span>;
      case 'divider': return <hr />;
      case 'spacer': return <div className="page-spacer" aria-label="Space" />;
      case 'products': return catalog;
    }
  }

  return <section className="page-builder" aria-label="Visual page editor">
    <div className="builder-toolbar">
      <div><h4>Page studio</h4><small>{blocks.length}/40 elements</small></div>
      <div className="builder-toolbar-actions">
        <button type="button" className="secondary" disabled={disabled || !history.past.length} onClick={() => undo(false)}>Undo</button>
        <button type="button" className="secondary" disabled={disabled || !history.future.length} onClick={() => undo(true)}>Redo</button>
        <button type="button" className="secondary" aria-pressed={mobile} onClick={() => setMobile(value => !value)}>Phone preview</button>
        <button type="button" className="secondary" aria-pressed={preview} onClick={() => setPreview(value => !value)}>{preview ? 'Back to editing' : 'Preview page'}</button>
      </div>
    </div>
    <p>Drag elements above, below, or to the left/right of a single element. Each row holds up to two elements of any type.
      On phones, paired elements stack. Use Add placement or Place beside previous without dragging. Changes publish with this new deployment.</p>
    <div className={`builder-layout${preview ? ' is-preview' : ''}`}>
      {!preview && <aside className="builder-palette" aria-label="Add elements">
        <h5>Add elements</h5>
        <p className="builder-palette-hint">Drag onto your page or click to add.</p>
        <div className="builder-palette-grid">
          {blockTypes.map(item => <button key={item.type} type="button" className="builder-palette-tile" draggable={!disabled}
            disabled={disabled || blocks.length >= 40 || (item.type === 'products' && blocks.some(block => block.type === 'products'))}
            onDragStart={event => startDrag(event, { type: item.type })} onDragEnd={() => { drag.current = null; setDropTarget(null); }}
            onClick={() => add(item.type, blocks.length, canInsertBeside && selected && placement !== 'row' ? { id: selected.id, side: placement } : undefined)} aria-label={`Add ${item.label}`}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"
              aria-hidden="true" focusable="false">{elementIcons[item.type]}</svg>
            <span>{item.label}</span>
          </button>)}
        </div>
        <label>Add placement<select value={canInsertBeside ? placement : 'row'} disabled={disabled}
          onChange={event => {
            const value = event.target.value;
            if (value === 'row' || value === 'left' || value === 'right') setPlacement(value);
          }}>
          <option value="row">New row</option>
          <option value="left" disabled={!canInsertBeside}>Left of selected</option>
          <option value="right" disabled={!canInsertBeside}>Right of selected</option>
        </select></label>
      </aside>}
      <div className="builder-stage">
        <div className={`builder-canvas generated-site site-style-${style}${mobile ? ' phone-preview' : ''}`}
          role="region" aria-label="Page canvas">
          <div className="site-content">
            {[...rows, []].map((row, rowIndex) => {
              const index = row.length ? blocks.indexOf(row[0]) : blocks.length;
              return <Fragment key={row[0]?.id ?? 'end'}>
                {!preview && <div className={`builder-drop-zone${dropTarget === `row-${index}` ? ' is-over' : ''}`}
                  role="region" aria-label={`Drop element at position ${index + 1}`}
                  onDragOver={event => { if (!disabled && drag.current) { event.preventDefault(); setDropTarget(`row-${index}`); } }}
                  onDragLeave={() => setDropTarget(current => current === `row-${index}` ? null : current)}
                  onDrop={event => drop(event, index)}>Drop here</div>}
                {!!row.length && <div className={`page-row${row.length === 2 ? ' page-row-pair' : ''} builder-row`} role="group" aria-label={`Page row ${rowIndex + 1}`}>
                  {!preview && row.length === 1 && (['left', 'right'] as const).map(side =>
                    <div key={side} className={`builder-side-drop builder-side-${side}${dropTarget === `${side}-${row[0].id}` ? ' is-over' : ''}`}
                      role="region" aria-label={`Drop ${side} of element ${index + 1}`}
                      onDragOver={event => { if (!disabled && drag.current) { event.preventDefault(); setDropTarget(`${side}-${row[0].id}`); } }}
                      onDragLeave={() => setDropTarget(null)}
                      onDrop={event => drop(event, index, { id: row[0].id, side })}>{side === 'left' ? 'Left' : 'Right'}</div>)}
                  {row.map(block => {
                    const index = blocks.indexOf(block);
                    return <div key={block.id} className={`builder-element${!preview && selectedId === block.id ? ' is-selected' : ''}`}>
                  {!preview && <div className="builder-element-toolbar">
                    <button type="button" className="secondary" disabled={disabled} draggable={!disabled}
                      aria-label={`Drag element ${index + 1}`} onDragStart={event => startDrag(event, { id: block.id })}
                      onDragEnd={() => { drag.current = null; setDropTarget(null); }}>Drag</button>
                    <button type="button" className="secondary" disabled={disabled} aria-pressed={selectedId === block.id}
                      onClick={() => setSelectedId(block.id)} aria-label={`Edit element ${index + 1}`}>Edit {blockTypes.find(item => item.type === block.type)?.label}</button>
                    <button type="button" className="secondary" disabled={disabled || index === 0} aria-label={`Move element ${index + 1} up`}
                      onClick={() => commit(moveBlock(blocks, block.id, index - 1), `Element moved to position ${index}.`)}>Up</button>
                    <button type="button" className="secondary" disabled={disabled || index === blocks.length - 1} aria-label={`Move element ${index + 1} down`}
                      onClick={() => commit(moveBlock(blocks, block.id, index + 2), `Element moved to position ${index + 2}.`)}>Down</button>
                  </div>}
                  <div className={`page-block page-block-${block.type} page-align-${block.align} page-size-${block.size}`}
                    onClick={() => { if (!preview) setSelectedId(block.id); }}>{renderBlock(block)}</div>
                </div>;
                  })}
                </div>}
              </Fragment>;
            })}
            {!blocks.length && <p className="builder-empty">Your page starts here. Add a heading, image, or product catalog.</p>}
          </div>
        </div>
      </div>
      {!preview && <aside className="builder-properties" aria-label="Element settings">
        <h5>Element settings</h5>
        {!selected ? <p>Select an element on the page.</p> : <fieldset disabled={disabled}>
          <legend>{blockTypes.find(item => item.type === selected.type)?.label}</legend>
          {['heading', 'text', 'button'].includes(selected.type) && <label>Element text<textarea rows={4} maxLength={selected.type === 'text' ? 2000 : 80}
            value={selected.text} onChange={event => change('text', event.target.value)} /></label>}
          {(selected.type === 'image' || selected.type === 'button') && <label>{selected.type === 'image' ? 'Image URL' : 'Button URL'}<input type="url" maxLength={2000}
            placeholder="https://..." value={selected.url} onChange={event => change('url', event.target.value)} /></label>}
          {selected.type === 'image' && <label>Image alternative text<input maxLength={160} value={selected.alt} onChange={event => change('alt', event.target.value)} /></label>}
          {selected.type === 'products' ? <p>Edit products in the catalog fields. The deployed buttons use your own Stripe Payment Links.</p> : <>
            <label>Alignment<select value={selected.align} onChange={event => change('align', event.target.value)}>
              <option value="left">Left</option><option value="center">Center</option><option value="right">Right</option>
            </select></label>
            <label>Element size<select value={selected.size} onChange={event => change('size', event.target.value)}>
              <option value="small">Small</option><option value="medium">Medium</option><option value="large">Large</option>
            </select></label>
          </>}
          <button type="button" className="secondary" disabled={!canPairPrevious} onClick={() => {
            if (previous) commit(placeBeside(blocks, selected, previous.id, 'right'), 'Element placed beside the previous element.');
          }}>Place beside previous</button>
          <button type="button" className="secondary" disabled={selectedRow?.length !== 2}
            onClick={() => commit(splitRow(blocks, selected.id), 'Row stacked vertically.')}>Stack row vertically</button>
          <button type="button" className="secondary" disabled={blocks.length >= 40 || selected.type === 'products'} onClick={() => {
            const copy = { ...selected, id: crypto.randomUUID(), beside: false };
            const index = blocks.findIndex(block => block.id === selected.id);
            const afterRow = index + (blocks[index + 1]?.beside ? 2 : 1);
            commit(insertBlock(blocks, copy, afterRow), 'Element duplicated.');
            setSelectedId(copy.id);
          }}>Duplicate element</button>
          <button type="button" className="secondary" onClick={() => {
            commit(removeBlock(blocks, selected.id), 'Element deleted.');
            setSelectedId(null);
          }}>Delete element</button>
        </fieldset>}
      </aside>}
    </div>
    <p className="builder-status" role="status" aria-live="polite">{announcement}</p>
  </section>;
}
