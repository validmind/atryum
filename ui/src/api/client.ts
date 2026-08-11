import axios from 'axios';
import type { AxiosError, InternalAxiosRequestConfig } from 'axios';

import { getAdminAccessToken, refreshAdminAccessToken } from '../auth/adminAuth';

export const atryumApi = axios.create({
  headers: { 'Content-Type': 'application/json' },
});

export const apiErrorMessage = (err: unknown, fallback: string): string => {
  if (typeof err === 'object' && err !== null && 'response' in err) {
    const data = (err as {
      response?: { data?: { error?: string | { message?: unknown } } };
    }).response?.data;
    const apiError = data?.error;
    if (typeof apiError === 'string' && apiError) return apiError;
    if (
      typeof apiError === 'object' &&
      apiError !== null &&
      typeof apiError.message === 'string' &&
      apiError.message
    ) {
      return apiError.message;
    }
  }
  if (err instanceof Error && err.message) return err.message;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    const msg = (err as { message: unknown }).message;
    if (typeof msg === 'string' && msg) return msg;
  }
  return fallback;
};

type RetryableRequestConfig = InternalAxiosRequestConfig & {
  _atryumAuthRetried?: boolean;
};

atryumApi.interceptors.request.use(async (config) => {
  const token = await getAdminAccessToken();
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

atryumApi.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const config = error.config as RetryableRequestConfig | undefined;
    if (error.response?.status !== 401 || !config || config._atryumAuthRetried) {
      throw error;
    }
    const token = await refreshAdminAccessToken();
    if (!token) throw error;
    config._atryumAuthRetried = true;
    config.headers.Authorization = `Bearer ${token}`;
    return atryumApi.request(config);
  },
);
