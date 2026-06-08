import React from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';

import Layout from './components/Layout';
import Invocations from './pages/Invocations';
import Agents from './pages/Agents';
import Servers from './pages/Servers';
import Rules from './pages/Rules';

const App: React.FC = () => (
  <Layout>
    <Routes>
      <Route index element={<Navigate to="/invocations" replace />} />
      <Route path="/invocations" element={<Invocations />} />
      <Route path="/agents" element={<Agents />} />
      <Route path="/servers" element={<Servers />} />
      <Route path="/rules" element={<Rules />} />
    </Routes>
  </Layout>
);

export default App;
