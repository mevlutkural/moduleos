import { toast } from "sonner";
import { AppError } from "../errors/app.error";
import { ApiError } from "../errors/api.error";
import { ValidationError } from "../errors/validation.error";

import i18next from "i18next";

export class NotificationManager {
  static handle(error: unknown) {
    if (!(error instanceof AppError)) {
      console.error("Unmanaged error:", error);
      toast.error(i18next.t("common.error"));
      return;
    }

    if (error instanceof ValidationError) return;

    if (error instanceof ApiError) {
      toast.error(error.message);
    }
  }
}
