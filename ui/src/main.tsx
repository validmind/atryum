import { createRoot } from 'react-dom/client';

import { createAtryumApp } from './createAtryumApp';

const container = document.getElementById('root');
const root = createRoot(container!);

root.render(createAtryumApp());
