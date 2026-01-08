import axios, {
  type AxiosInstance,
  type InternalAxiosRequestConfig,
} from "axios";
import qs from "qs";
import { CONFIG } from "../constants/config";
import { setupInterceptors } from "./setup-interceptors";

const createApiClient = (): AxiosInstance => {
  const client = axios.create({
    baseURL: CONFIG.API_BASE_URL,
    timeout: CONFIG.API_TIMEOUT,
    headers: {
      "Content-Type": "application/json",
    },
    paramsSerializer: (params) => {
      return qs.stringify(params, { arrayFormat: "brackets" });
    },
  });

  if (import.meta.env.DEV) {
    client.interceptors.request.use((config: InternalAxiosRequestConfig) => {
      config.metadata = { startTime: performance.now() };

      const { method, url, params, data } = config;
      console.groupCollapsed(
        `%c🚀 [API] ${method?.toUpperCase()} ${url}`,
        "color: #3b82f6; font-weight: bold;"
      );
      if (params) console.log("Params:", params);
      if (data) console.log("Body:", data);
      console.groupEnd();

      return config;
    });

    client.interceptors.response.use(
      (response) => {
        const { config, status, data } = response;
        const startTime = config.metadata?.startTime || 0;
        const duration = (performance.now() - startTime).toFixed(2);

        console.groupCollapsed(
          `%c✅ [${status}] ${config.method?.toUpperCase()} ${
            config.url
          } (${duration}ms)`,
          "color: #10b981; font-weight: bold;"
        );
        console.log("Response:", data);
        console.groupEnd();

        return response;
      },
      (error) => {
        const { config, response } = error;
        const startTime = config?.metadata?.startTime || 0;
        const duration = (performance.now() - startTime).toFixed(2);

        console.groupCollapsed(
          `%c🔥 [${
            response?.status || "ERR"
          }] ${config?.method?.toUpperCase()} ${config?.url} (${duration}ms)`,
          "color: #ef4444; font-weight: bold;"
        );
        console.error("Error:", error);
        if (response?.data) console.log("Error Response:", response.data);
        console.groupEnd();

        return Promise.reject(error);
      }
    );
  }

  return client;
};

export const apiClient = createApiClient();
setupInterceptors(apiClient);

export type ApiClientType = typeof apiClient;

export type { AxiosRequestConfig, AxiosResponse, AxiosError } from "axios";
