import { newBlock } from './pageBlocks';
import type { PageBlock } from './pageBlocks';

export type StartupSectionId = 'hero' | 'features' | 'pricing' | 'testimonials' | 'faq';

export interface StartupSection {
  id: StartupSectionId;
  label: string;
}

export const startupSections = [
  { id: 'hero', label: 'Hero' },
  { id: 'features', label: 'Features' },
  { id: 'pricing', label: 'Pricing' },
  { id: 'testimonials', label: 'Testimonials' },
  { id: 'faq', label: 'FAQ' },
] as const satisfies readonly StartupSection[];

function block(type: 'heading' | 'text' | 'button', text: string, beside = false): PageBlock {
  return { ...newBlock(type), text, beside };
}

export function newStartupSection(id: StartupSectionId): PageBlock[] {
  switch (id) {
    case 'hero':
      return [
        { ...block('heading', 'A simpler way to move your idea forward'), size: 'large', align: 'center' },
        { ...block('text', 'Introduce your product, who it helps, and the everyday problem it solves.'), align: 'center' },
        { ...block('button', 'Get started'), align: 'center' },
      ];
    case 'features':
      return [
        block('heading', 'Built around your next step'),
        block('text', 'Show the practical benefits that make your product useful.'),
        block('heading', 'Less busywork'),
        block('heading', 'More clarity', true),
        block('text', 'Explain how your product makes a routine task easier.'),
        block('text', 'Describe how it helps people see what to do next.', true),
      ];
    case 'pricing':
      return [
        block('heading', 'Choose your starting point'),
        block('text', 'Add your actual prices, included benefits, and billing terms before publishing.'),
        block('heading', 'Starter'),
        block('heading', 'Team', true),
        block('text', 'For individual projects. Add the price and what is included.'),
        block('text', 'For shared projects. Add the price and what is included.', true),
        block('button', 'Choose Starter'),
        block('button', 'Choose Team', true),
      ];
    case 'testimonials':
      return [
        block('heading', 'Stories from your customers'),
        block('text', 'Replace these writing prompts with approved customer quotes and attribution.'),
        block('text', 'Customer quote: describe a task that became easier and why it mattered.'),
        block('text', 'Customer quote: share a specific improvement in an everyday workflow.', true),
        block('text', 'Add customer name and role'),
        block('text', 'Add customer name and role', true),
      ];
    case 'faq':
      return [
        block('heading', 'A few things to know'),
        block('text', 'Answer the questions people ask before taking their first step.'),
        block('heading', 'How do I get started?'),
        block('text', 'Describe the first step and what someone needs to prepare.'),
        block('heading', 'Where can I find help?'),
        block('text', 'Explain your support options and when people can expect a reply.'),
      ];
    default:
      throw new RangeError(`Unknown startup section: ${id}`);
  }
}
