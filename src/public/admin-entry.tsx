import { createRoot } from 'react-dom/client';
import { Admin } from './pages/Admin.js';

const root = document.getElementById('root');
if (!root) throw new Error('Admin root element is missing.');
createRoot(root).render(<Admin />);
