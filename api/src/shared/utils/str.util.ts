import { v4 } from 'uuid';

export class StrUtil {
  static generateUuid(): string {
    try {
      return v4();
    } catch (error) {
      console.error('UUID generation failed:', error);
      throw error;
    }
  }
}
