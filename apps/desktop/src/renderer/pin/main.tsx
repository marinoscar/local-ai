// Shell page "pin" (docs/specs/desktop.md section 3.1, 4.6): the modal PIN prompt the main process opens.
import { mountPage } from '../shared/mount';
import { PinPage } from './PinPage';

mountPage(<PinPage bridge={window.localaiPin} search={window.location.search} />);
