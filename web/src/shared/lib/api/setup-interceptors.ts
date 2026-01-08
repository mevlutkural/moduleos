import type {
  AxiosInstance,
  AxiosResponse,
  InternalAxiosRequestConfig,
} from "axios";
import { ErrorHandler } from "../../errors/error-handler";

export const setupInterceptors = (client: AxiosInstance) => {
  client.interceptors.request.use(
    (config: InternalAxiosRequestConfig) => {
      return config;
    },
    (error) => {
      return Promise.reject(ErrorHandler.handle(error));
    }
  );

  client.interceptors.response.use(
    (response: AxiosResponse) => {
      return response;
    },
    (error: unknown) => {
      return Promise.reject(ErrorHandler.handle(error));
    }
  );
};
