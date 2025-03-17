import { apiService } from './api-service.server';
import type { RequestEvent } from '@sveltejs/kit';

// Log levels
export enum LogLevel {
  DEBUG = 'debug',
  INFO = 'info',
  WARN = 'warn',
  ERROR = 'error',
  FATAL = 'fatal'
}

// Log level hierarchy for filtering
const logLevelHierarchy = {
  [LogLevel.DEBUG]: 0,
  [LogLevel.INFO]: 1,
  [LogLevel.WARN]: 2,
  [LogLevel.ERROR]: 3,
  [LogLevel.FATAL]: 4
};

class Logger {
  private currentLevel: LogLevel = LogLevel.INFO;
  private initialized: boolean = false;

  constructor() {
    // Try to get log level from localStorage if available (client-side only)
    if (typeof window !== 'undefined' && window.localStorage) {
      const savedLevel = localStorage.getItem('logLevel');
      if (savedLevel && Object.values(LogLevel).includes(savedLevel as LogLevel)) {
        this.currentLevel = savedLevel as LogLevel;
      }
    }
  }

  /**
   * Initialize the logger by fetching the log level from the server
   */
  async init(event?: RequestEvent): Promise<void> {
    if (this.initialized) return;
    
    try {
      const serverLogLevel = await apiService.config.getLogLevel(event);
      this.setLevel(serverLogLevel as LogLevel);
      this.initialized = true;
    } catch (error) {
      console.error('Failed to initialize logger with server log level:', error);
    }
  }

  /**
   * Set the current log level
   */
  setLevel(level: LogLevel): void {
    this.currentLevel = level;
    
    // Save to localStorage if available (client-side only)
    if (typeof window !== 'undefined' && window.localStorage) {
      localStorage.setItem('logLevel', level);
    }
    
    console.debug(`Log level set to: ${level}`);
  }

  /**
   * Get the current log level
   */
  getLevel(): LogLevel {
    return this.currentLevel;
  }

  /**
   * Check if a log level should be displayed based on current level
   */
  private shouldLog(level: LogLevel): boolean {
    return logLevelHierarchy[level] >= logLevelHierarchy[this.currentLevel];
  }

  /**
   * Log a debug message
   */
  debug(message: string, ...args: unknown[]): void {
    if (this.shouldLog(LogLevel.DEBUG)) {
      console.debug(message, ...args);
    }
  }

  /**
   * Log an info message
   */
  info(message: string, ...args: unknown[]): void {
    if (this.shouldLog(LogLevel.INFO)) {
      console.info(message, ...args);
    }
  }

  /**
   * Log a warning message
   */
  warn(message: string, ...args: unknown[]): void {
    if (this.shouldLog(LogLevel.WARN)) {
      console.warn(message, ...args);
    }
  }

  /**
   * Log an error message
   */
  error(message: string, ...args: unknown[]): void {
    if (this.shouldLog(LogLevel.ERROR)) {
      console.error(message, ...args);
    }
  }

  /**
   * Log a fatal message (uses console.error)
   */
  fatal(message: string, ...args: unknown[]): void {
    if (this.shouldLog(LogLevel.FATAL)) {
      console.error('[FATAL]', message, ...args);
    }
  }
}

// Export a singleton instance
export const logger = new Logger();

// Export convenience methods
export const debug = (message: string, ...args: unknown[]): void => logger.debug(message, ...args);
export const info = (message: string, ...args: unknown[]): void => logger.info(message, ...args);
export const warn = (message: string, ...args: unknown[]): void => logger.warn(message, ...args);
export const error = (message: string, ...args: unknown[]): void => logger.error(message, ...args);
export const fatal = (message: string, ...args: unknown[]): void => logger.fatal(message, ...args);

export default logger; 