import React from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';

import Layout from './components/Layout';
import Invocations from './pages/Invocations';
import Agents from './pages/Agents';
import Servers from './pages/Servers';
import Rules from './pages/Rules';
import Settings from './pages/Settings';
import { useApprovalNotifications } from './hooks/useApprovalNotifications';

const App: React.FC = () => {
  useApprovalNotifications();

  return (
    <Layout>
      <Routes>
        <Route index element={<Navigate to="/invocations" replace />} />
        <Route path="/invocations" element={<Invocations />} />
        <Route path="/agents" element={<Agents />} />
        <Route path="/servers" element={<Servers />} />
        <Route path="/rules" element={<Rules />} />
        <Route path="/settings" element={<Settings />} />
      </Routes>
    </Layout>
  );
};

export default App;
