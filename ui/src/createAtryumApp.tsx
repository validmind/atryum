import React from 'react';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from 'react-query';
import { ChakraProvider } from '@chakra-ui/react';

import App from './App';
import { AdminAuthProvider } from './auth/adminAuth';
import { theme } from './theme';

export function createAtryumApp(): React.ReactElement {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: 1,
        refetchOnWindowFocus: false,
      },
    },
  });

  // Vite's base '/ui/' and this router basename must remain in sync.
  return (
    <React.StrictMode>
      <BrowserRouter basename="/ui">
        <QueryClientProvider client={queryClient}>
          <ChakraProvider theme={theme}>
            <AdminAuthProvider>
              <App />
            </AdminAuthProvider>
          </ChakraProvider>
        </QueryClientProvider>
      </BrowserRouter>
    </React.StrictMode>
  );
}
