import { createRoot } from 'react-dom/client';
import { Storefront } from './pages/Storefront.js';

const root = document.getElementById('root');
if (!root) throw new Error('Storefront root element is missing.');
createRoot(root).render(<Storefront />);
