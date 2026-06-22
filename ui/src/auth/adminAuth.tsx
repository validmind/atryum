import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import {
  Box,
  Button,
  Center,
  FormControl,
  FormLabel,
  Heading,
  Image,
  Select,
  Spinner,
  Stack,
  Text,
} from '@chakra-ui/react';
import {
  User,
  UserManager,
  WebStorageStateStore,
  type UserManagerSettings,
} from 'oidc-client-ts';

export interface AdminAuthProviderMetadata {
  id: string;
  name: string;
  provider: string;
  issuer: string;
  authority: string;
  audience: string;
  client_id: string;
  scopes: string;
  redirect_uri: string;
}

interface AdminAuthConfig {
  providers: AdminAuthProviderMetadata[];
}

type AuthStatus = 'loading' | 'disabled' | 'authenticated' | 'unauthenticated' | 'error';

interface AdminAuthContextValue {
  status: AuthStatus;
  providers: AdminAuthProviderMetadata[];
  selectedProvider: AdminAuthProviderMetadata | null;
  user: User | null;
  error: string | null;
  signIn: (providerId?: string) => Promise<void>;
  signOut: () => Promise<void>;
  selectProvider: (providerId: string) => void;
  getAccessToken: () => Promise<string | null>;
  refreshAccessToken: () => Promise<string | null>;
}

const AdminAuthContext = createContext<AdminAuthContextValue | null>(null);

let activeManager: UserManager | null = null;
let activeUser: User | null = null;
let authCallbackPromise: Promise<void> | null = null;

const SELECTED_PROVIDER_KEY = 'atryum.adminAuth.provider';

const fetchConfig = async (): Promise<AdminAuthConfig> => {
  const res = await fetch('/api/v1/admin-auth/config', {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) throw new Error(`admin auth config failed (${res.status})`);
  return (await res.json()) as AdminAuthConfig;
};

const createManager = (provider: AdminAuthProviderMetadata): UserManager => {
  const redirectURI = `${window.location.origin}/ui/auth/callback`;
  const settings: UserManagerSettings = {
    authority: provider.authority || provider.issuer,
    client_id: provider.client_id,
    redirect_uri: redirectURI,
    response_type: 'code',
    scope: provider.scopes,
    automaticSilentRenew: true,
    loadUserInfo: true,
    userStore: new WebStorageStateStore({ store: window.localStorage }),
    extraQueryParams: provider.audience ? { audience: provider.audience } : undefined,
  };
  return new UserManager(settings);
};

const managerForCallback = async (): Promise<UserManager> => {
  if (activeManager) return activeManager;
  const config = await fetchConfig();
  const storedId = window.localStorage.getItem(SELECTED_PROVIDER_KEY);
  const provider =
    config.providers?.find((item) => item.id === storedId) ??
    config.providers?.[0];
  if (!provider) throw new Error('admin auth provider is not configured');
  activeManager = createManager(provider);
  return activeManager;
};

const completeSignInCallback = async (): Promise<void> => {
  const manager = await managerForCallback();
  activeUser = await manager.signinRedirectCallback();
  window.history.replaceState({}, document.title, '/ui/');
  window.location.assign('/ui/');
};

const isUsableUser = (user: User | null): user is User =>
  user !== null && Boolean(user.access_token) && !user.expired;

export const getAdminAccessToken = async (): Promise<string | null> => {
  if (!activeManager) return null;
  if (isUsableUser(activeUser)) return activeUser.access_token;
  const user = await activeManager.getUser();
  activeUser = user;
  if (isUsableUser(user)) return user.access_token;
  return null;
};

export const refreshAdminAccessToken = async (): Promise<string | null> => {
  if (!activeManager) return null;
  try {
    activeUser = await activeManager.signinSilent();
  } catch {
    activeUser = await activeManager.getUser();
  }
  if (isUsableUser(activeUser)) return activeUser.access_token;
  // Silent renew failed and no usable token remains. Clear the session so the
  // provider's userUnloaded event drives the UI back to sign-in instead of
  // leaving a stale "authenticated" shell with dead data.
  try {
    await activeManager.removeUser();
  } catch {
    /* best effort */
  }
  activeUser = null;
  return null;
};

const LoadingScreen: React.FC = () => (
  <Center minH="100vh" bg="background.page">
    <Stack align="center" gap={4}>
      <Spinner color="brand.base" />
      <Text color="text.secondary">Loading Atryum</Text>
    </Stack>
  </Center>
);

const SignInScreen: React.FC<{
  providers: AdminAuthProviderMetadata[];
  selectedProvider: AdminAuthProviderMetadata | null;
  error: string | null;
  onSelect: (providerId: string) => void;
  onSignIn: (providerId: string) => void;
}> = ({ providers, selectedProvider, error, onSelect, onSignIn }) => (
  <Center minH="100vh" bg="background.page" px={6}>
    <Box w="full" maxW="420px">
      <Stack gap={8}>
        <Stack gap={4} align="center">
          <Image src="/ui/atryum-logo.svg" alt="Atryum" h="56px" objectFit="contain" />
          <Heading size="md" color="text.heading">
            Atryum
          </Heading>
        </Stack>
        <Stack gap={4}>
          {providers.length > 1 ? (
            <FormControl>
              <FormLabel color="text.base">Identity provider</FormLabel>
              <Select
                value={selectedProvider?.id ?? ''}
                onChange={(event) => onSelect(event.target.value)}>
                {providers.map((provider) => (
                  <option key={provider.id} value={provider.id}>
                    {provider.name}
                  </option>
                ))}
              </Select>
            </FormControl>
          ) : null}
          {error ? <Text color="text.error">{error}</Text> : null}
          <Button
            variant="primary"
            onClick={() => {
              if (selectedProvider) onSignIn(selectedProvider.id);
            }}
            isDisabled={!selectedProvider}>
            Sign in
          </Button>
        </Stack>
      </Stack>
    </Box>
  </Center>
);

export const AdminAuthProvider: React.FC<{ children: React.ReactNode }> = ({
  children,
}) => {
  const [status, setStatus] = useState<AuthStatus>('loading');
  const [providers, setProviders] = useState<AdminAuthProviderMetadata[]>([]);
  const [selectedProvider, setSelectedProvider] =
    useState<AdminAuthProviderMetadata | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const [error, setError] = useState<string | null>(null);

  const installManager = useCallback(async (provider: AdminAuthProviderMetadata) => {
    const manager = createManager(provider);
    activeManager = manager;
    const existingUser = await manager.getUser();
    activeUser = existingUser;
    setUser(existingUser);
    setStatus(isUsableUser(existingUser) ? 'authenticated' : 'unauthenticated');
  }, []);

  const selectProvider = useCallback(
    (providerId: string) => {
      const provider = providers.find((item) => item.id === providerId) ?? null;
      if (!provider) return;
      window.localStorage.setItem(SELECTED_PROVIDER_KEY, provider.id);
      setSelectedProvider(provider);
      activeUser = null;
      activeManager = createManager(provider);
      setUser(null);
      setStatus('unauthenticated');
    },
    [providers],
  );

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const config = await fetchConfig();
        if (cancelled) return;
        const configuredProviders = config.providers ?? [];
        setProviders(configuredProviders);
        if (configuredProviders.length === 0) {
          activeManager = null;
          activeUser = null;
          setStatus('disabled');
          return;
        }
        const storedId = window.localStorage.getItem(SELECTED_PROVIDER_KEY);
        const provider =
          configuredProviders.find((item) => item.id === storedId) ??
          configuredProviders[0];
        setSelectedProvider(provider);
        await installManager(provider);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : 'failed to load auth config');
        setStatus('error');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [installManager]);

  useEffect(() => {
    if (!activeManager) return;
    const manager = activeManager;
    const handleUserLoaded = (nextUser: User) => {
      activeUser = nextUser;
      setUser(nextUser);
      setStatus('authenticated');
    };
    const handleUserUnloaded = () => {
      activeUser = null;
      setUser(null);
      setStatus('unauthenticated');
    };
    manager.events.addUserLoaded(handleUserLoaded);
    manager.events.addUserUnloaded(handleUserUnloaded);
    manager.events.addAccessTokenExpired(handleUserUnloaded);
    return () => {
      manager.events.removeUserLoaded(handleUserLoaded);
      manager.events.removeUserUnloaded(handleUserUnloaded);
      manager.events.removeAccessTokenExpired(handleUserUnloaded);
    };
  }, [selectedProvider]);

  const signIn = useCallback(
    async (providerId?: string) => {
      let provider = selectedProvider;
      if (providerId) {
        provider = providers.find((item) => item.id === providerId) ?? null;
        if (provider) {
          window.localStorage.setItem(SELECTED_PROVIDER_KEY, provider.id);
          setSelectedProvider(provider);
          activeManager = createManager(provider);
        }
      }
      if (!provider || !activeManager) return;
      try {
        setError(null);
        await activeManager.signinRedirect();
      } catch (err) {
        setError(err instanceof Error ? err.message : 'failed to start sign in');
        setStatus('unauthenticated');
      }
    },
    [providers, selectedProvider],
  );

  const signOut = useCallback(async () => {
    if (activeManager) {
      await activeManager.removeUser();
    }
    activeUser = null;
    setUser(null);
    setStatus(providers.length > 0 ? 'unauthenticated' : 'disabled');
  }, [providers.length]);

  const value = useMemo<AdminAuthContextValue>(
    () => ({
      status,
      providers,
      selectedProvider,
      user,
      error,
      signIn,
      signOut,
      selectProvider,
      getAccessToken: getAdminAccessToken,
      refreshAccessToken: refreshAdminAccessToken,
    }),
    [error, providers, selectProvider, selectedProvider, signIn, signOut, status, user],
  );

  if (window.location.pathname === '/ui/auth/callback') {
    return (
      <AdminAuthContext.Provider value={value}>
        <AuthCallback />
      </AdminAuthContext.Provider>
    );
  }

  if (status === 'loading') return <LoadingScreen />;
  if (status === 'error') {
    return (
      <SignInScreen
        providers={providers}
        selectedProvider={selectedProvider}
        error={error}
        onSelect={selectProvider}
        onSignIn={(providerId) => void signIn(providerId)}
      />
    );
  }
  if (status === 'unauthenticated') {
    return (
      <SignInScreen
        providers={providers}
        selectedProvider={selectedProvider}
        error={error}
        onSelect={selectProvider}
        onSignIn={(providerId) => void signIn(providerId)}
      />
    );
  }

  return (
    <AdminAuthContext.Provider value={value}>{children}</AdminAuthContext.Provider>
  );
};

const AuthCallback: React.FC = () => {
  const [message, setMessage] = useState('Completing sign in');
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!authCallbackPromise) {
      authCallbackPromise = completeSignInCallback();
    }
    authCallbackPromise.catch((err) => {
      authCallbackPromise = null;
      setMessage('Sign in failed');
      setError(err instanceof Error ? err.message : 'No matching sign-in state was found');
    });
  }, []);

  return (
    <Center minH="100vh" bg="background.page">
      <Stack align="center" gap={4}>
        {error ? null : <Spinner color="brand.base" />}
        <Text color="text.secondary">{message}</Text>
        {error ? (
          <>
            <Text color="text.error" textAlign="center">
              {error}
            </Text>
            <Button
              variant="primary"
              onClick={() => {
                window.history.replaceState({}, document.title, '/ui/');
                window.location.assign('/ui/');
              }}>
              Back to sign in
            </Button>
          </>
        ) : null}
      </Stack>
    </Center>
  );
};

export const useAdminAuth = (): AdminAuthContextValue => {
  const context = useContext(AdminAuthContext);
  if (!context) throw new Error('useAdminAuth must be used inside AdminAuthProvider');
  return context;
};
